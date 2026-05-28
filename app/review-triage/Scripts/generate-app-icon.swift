#!/usr/bin/env swift

// Renders the app icon from an SF Symbol on a Valian-green gradient and writes
// the full set of macOS icon sizes (16–512 pt at 1x and 2x) plus a matching
// Contents.json into AppIcon.appiconset.
//
// A macOS app icon needs every standard slot present as its own PNG — actool
// does NOT downscale a single 1024 image for the `mac` idiom (it just warns
// "unassigned child" and ships no icon). Each slot is re-rendered at its native
// pixel size so small icons stay crisp instead of being blurry downscales.
//
// Re-run when you want to change the symbol, weight, or background:
//   swift app/review-triage/Scripts/generate-app-icon.swift
//
// Symbol candidates that read well at icon scale:
//   checkmark.diamond.fill, checklist.checked, text.badge.checkmark,
//   magnifyingglass.circle.fill, list.bullet.indent

import AppKit
import Foundation

let symbolName = "checkmark.diamond.fill"

let iconsetDir = "/Users/fp/code/valian/homebrew-tools/app/review-triage/ReviewTriage/Resources/Assets.xcassets/AppIcon.appiconset"

// (point size, scale) → the standard macOS app-icon slots.
let slots: [(size: Int, scale: Int)] = [
    (16, 1), (16, 2),
    (32, 1), (32, 2),
    (128, 1), (128, 2),
    (256, 1), (256, 2),
    (512, 1), (512, 2),
]

func render(pixels: Int) -> Data {
    guard let bitmap = NSBitmapImageRep(
        bitmapDataPlanes: nil,
        pixelsWide: pixels,
        pixelsHigh: pixels,
        bitsPerSample: 8,
        samplesPerPixel: 4,
        hasAlpha: true,
        isPlanar: false,
        colorSpaceName: .deviceRGB,
        bytesPerRow: 0,
        bitsPerPixel: 32
    ), let context = NSGraphicsContext(bitmapImageRep: bitmap) else {
        FileHandle.standardError.write(Data("error: failed to allocate bitmap at \(pixels)px\n".utf8))
        exit(1)
    }

    NSGraphicsContext.saveGraphicsState()
    NSGraphicsContext.current = context

    let side = CGFloat(pixels)
    let rect = NSRect(x: 0, y: 0, width: side, height: side)
    let cornerRadius = side * 0.22 // matches Apple's macOS icon squircle
    NSBezierPath(roundedRect: rect, xRadius: cornerRadius, yRadius: cornerRadius).addClip()

    let gradient = NSGradient(colors: [
        NSColor(srgbRed: 0.06, green: 0.62, blue: 0.43, alpha: 1.0),
        NSColor(srgbRed: 0.02, green: 0.42, blue: 0.30, alpha: 1.0),
    ])!
    gradient.draw(in: rect, angle: -45)

    guard let baseSymbol = NSImage(systemSymbolName: symbolName, accessibilityDescription: nil) else {
        NSGraphicsContext.restoreGraphicsState()
        FileHandle.standardError.write(Data("error: SF Symbol '\(symbolName)' not found\n".utf8))
        exit(1)
    }
    let symbolConfig = NSImage.SymbolConfiguration(pointSize: side * 0.547, weight: .semibold)
        .applying(.init(paletteColors: [.white]))
    guard let symbol = baseSymbol.withSymbolConfiguration(symbolConfig) else {
        NSGraphicsContext.restoreGraphicsState()
        FileHandle.standardError.write(Data("error: failed to apply symbol configuration\n".utf8))
        exit(1)
    }

    let measured = symbol.size
    let symbolRect = NSRect(
        x: (side - measured.width) / 2,
        y: (side - measured.height) / 2,
        width: measured.width,
        height: measured.height
    )
    symbol.draw(in: symbolRect, from: .zero, operation: .sourceOver, fraction: 1.0)

    NSGraphicsContext.restoreGraphicsState()

    guard let png = bitmap.representation(using: .png, properties: [:]) else {
        FileHandle.standardError.write(Data("error: failed to encode PNG at \(pixels)px\n".utf8))
        exit(1)
    }
    return png
}

func filename(size: Int, scale: Int) -> String {
    scale == 1 ? "icon_\(size)x\(size).png" : "icon_\(size)x\(size)@\(scale)x.png"
}

// Cache renders by pixel size so the shared sizes (32, 256, 512) only draw once.
var cache: [Int: Data] = [:]
var images: [[String: String]] = []

for slot in slots {
    let pixels = slot.size * slot.scale
    let png = cache[pixels] ?? render(pixels: pixels)
    cache[pixels] = png
    let name = filename(size: slot.size, scale: slot.scale)
    try png.write(to: URL(fileURLWithPath: "\(iconsetDir)/\(name)"))
    images.append([
        "filename": name,
        "idiom": "mac",
        "scale": "\(slot.scale)x",
        "size": "\(slot.size)x\(slot.size)",
    ])
}

let contents: [String: Any] = [
    "images": images,
    "info": ["author": "xcode", "version": 1],
]
let json = try JSONSerialization.data(withJSONObject: contents, options: [.prettyPrinted, .sortedKeys])
try json.write(to: URL(fileURLWithPath: "\(iconsetDir)/Contents.json"))

print("wrote \(slots.count) PNGs + Contents.json into \(iconsetDir)")
