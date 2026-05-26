#!/usr/bin/env swift

// Renders the app icon from an SF Symbol on a Valian-green gradient and writes
// it as a single 1024×1024 PNG into the AppIcon.appiconset. Xcode's actool
// downscales to the other macOS sizes at build time.
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

let outputPath = "/Users/fp/code/valian/homebrew-tools/app/review-triage/ReviewTriage/Resources/Assets.xcassets/AppIcon.appiconset/AppIcon-1024.png"

let pixels = 1024
let cornerRadius: CGFloat = 224 // ~22% — matches Apple's macOS icon squircle

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
) else {
    FileHandle.standardError.write(Data("error: failed to allocate bitmap\n".utf8))
    exit(1)
}

guard let context = NSGraphicsContext(bitmapImageRep: bitmap) else {
    FileHandle.standardError.write(Data("error: failed to build graphics context\n".utf8))
    exit(1)
}

NSGraphicsContext.saveGraphicsState()
NSGraphicsContext.current = context

let rect = NSRect(x: 0, y: 0, width: pixels, height: pixels)
let squircle = NSBezierPath(roundedRect: rect, xRadius: cornerRadius, yRadius: cornerRadius)
squircle.addClip()

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
let symbolConfig = NSImage.SymbolConfiguration(pointSize: 560, weight: .semibold)
    .applying(.init(paletteColors: [.white]))
guard let symbol = baseSymbol.withSymbolConfiguration(symbolConfig) else {
    NSGraphicsContext.restoreGraphicsState()
    FileHandle.standardError.write(Data("error: failed to apply symbol configuration\n".utf8))
    exit(1)
}

let measured = symbol.size
let symbolRect = NSRect(
    x: (CGFloat(pixels) - measured.width) / 2,
    y: (CGFloat(pixels) - measured.height) / 2,
    width: measured.width,
    height: measured.height
)
symbol.draw(in: symbolRect, from: .zero, operation: .sourceOver, fraction: 1.0)

NSGraphicsContext.restoreGraphicsState()

guard let png = bitmap.representation(using: .png, properties: [:]) else {
    FileHandle.standardError.write(Data("error: failed to encode PNG\n".utf8))
    exit(1)
}

try png.write(to: URL(fileURLWithPath: outputPath))
print("wrote \(outputPath)")
