import SwiftUI
import AppKit

public enum AppSettingsDefaults {
    public static let textFontSize: Double = 13   // matches SwiftUI `.callout`/`.body` rendering at default Dynamic Type
    public static let codeFontSize: Double = 13
    public static let textFontFamily: String = "" // empty sentinel = use SF Pro (system default)
    public static let codeFontFamily: String = "" // empty sentinel = use SF Mono (system monospaced)

    public static let minFontSize: Double = 11
    public static let maxFontSize: Double = 24
}

public enum AppSettingsKeys {
    public static let textFontSize = "review-triage.textFontSize"
    public static let codeFontSize = "review-triage.codeFontSize"
    public static let textFontFamily = "review-triage.textFontFamily"
    public static let codeFontFamily = "review-triage.codeFontFamily"
}

/// Resolved fonts derived from the user's settings. Built once at the
/// `WindowGroup` root and injected via Environment so each view picks the
/// same fonts without re-reading `UserDefaults`.
///
/// The palette exposes both convenience properties (for the common combos
/// the current views already used — `.title3.weight(.semibold)`, `.caption`,
/// etc.) and the underlying `text(...)` / `monoCode(...)` builders for any
/// case the convenience layer doesn't cover.
public struct FontPalette: Equatable, Sendable {
    public let textSize: Double
    public let codeSize: Double
    public let textFamily: String
    public let codeFamily: String

    public init(textSize: Double, codeSize: Double, textFamily: String, codeFamily: String) {
        self.textSize = textSize
        self.codeSize = codeSize
        self.textFamily = textFamily
        self.codeFamily = codeFamily
    }

    public static let standard = FontPalette(
        textSize: AppSettingsDefaults.textFontSize,
        codeSize: AppSettingsDefaults.codeFontSize,
        textFamily: AppSettingsDefaults.textFontFamily,
        codeFamily: AppSettingsDefaults.codeFontFamily
    )

    // MARK: - Scaled point sizes (proportional to the user-chosen base)
    //
    // The offsets match the gaps between SwiftUI's semantic sizes at default
    // Dynamic Type: `.body` ≈ 13, `.title3` ≈ 20, `.title` ≈ 28, `.caption` ≈ 11.

    public var titleSize: Double { textSize + 11 }    // ≈ `.title`
    public var headingSize: Double { textSize + 7 }   // ≈ `.title3`
    public var bodySize: Double { textSize }          // ≈ `.body` / `.callout` / `.headline`
    public var subheadlineSize: Double { textSize - 1 }
    public var captionSize: Double { max(10, textSize - 2) }   // floor so it stays legible at min textSize

    // MARK: - Text builders

    /// Generic text font — most callers go through the convenience properties
    /// below; reach for this directly only when the existing combos don't fit.
    public func text(_ size: Double, weight: Font.Weight = .regular) -> Font {
        if textFamily.isEmpty {
            return .system(size: size, weight: weight)
        }
        return Font.custom(textFamily, size: size).weight(weight)
    }

    /// Monospaced font in the configured code family — for diff content and
    /// inline `file:line` citations.
    public func monoCode(_ size: Double, weight: Font.Weight = .regular) -> Font {
        if codeFamily.isEmpty {
            return .system(size: size, weight: weight, design: .monospaced)
        }
        return Font.custom(codeFamily, size: size).weight(weight)
    }

    // MARK: - Convenience properties (cover every callsite the existing views had)

    /// `.title.weight(.semibold)` — biggest heading, used for group summary screens.
    public var title: Font { text(titleSize, weight: .semibold) }

    /// `.title3.weight(.semibold)` — finding heading.
    public var heading: Font { text(headingSize, weight: .semibold) }

    /// `.headline` — emphasized body text, same size as body, semibold weight.
    public var headline: Font { text(bodySize, weight: .semibold) }

    /// `.body` / `.callout` — regular body text.
    public var body: Font { text(bodySize) }

    /// `.subheadline` — regular.
    public var subheadline: Font { text(subheadlineSize) }

    /// `.subheadline.weight(.medium)`
    public var subheadlineMedium: Font { text(subheadlineSize, weight: .medium) }

    /// `.subheadline.weight(.semibold)`
    public var subheadlineSemibold: Font { text(subheadlineSize, weight: .semibold) }

    /// `.caption` / `.caption2` — small secondary text.
    public var caption: Font { text(captionSize) }

    /// `.caption.weight(.medium)`
    public var captionMedium: Font { text(captionSize, weight: .medium) }

    /// `.caption2.weight(.semibold)` — badge labels.
    public var captionSemibold: Font { text(captionSize, weight: .semibold) }

    /// `.caption.monospacedDigit()` — proportional family with tabular figures,
    /// used in the diff gutter to keep line-number columns aligned.
    public var captionMonoDigits: Font { text(captionSize).monospacedDigit() }

    /// `.body.monospaced()` / `.system(.callout, design: .monospaced)` —
    /// the diff content and any multi-line code rendering.
    public var code: Font { monoCode(codeSize) }

    /// `.callout.monospaced()` — inline `file:line` citations, one size below
    /// the diff body so they don't compete visually with the code block.
    public var codeInline: Font { monoCode(max(10, codeSize - 1)) }

    /// `.caption.monospaced()` — tiny mono for the L13-L17 label in the
    /// discuss sheet.
    public var codeCaption: Font { monoCode(max(10, codeSize - 2)) }
}

// MARK: - Environment injection

private struct FontPaletteEnvironmentKey: EnvironmentKey {
    static let defaultValue: FontPalette = .standard
}

public extension EnvironmentValues {
    var fontPalette: FontPalette {
        get { self[FontPaletteEnvironmentKey.self] }
        set { self[FontPaletteEnvironmentKey.self] = newValue }
    }
}

// MARK: - Catalog of installed families, filtered for the two pickers

public enum FontCatalog {
    /// Available monospaced families (filtered via `NSFont.isFixedPitch`).
    /// Computed once on first access — the enumeration walks every installed
    /// font and isn't cheap.
    public static let monospacedFamilies: [String] = familiesMatching(fixedPitch: true)

    /// Available proportional families — UI/text candidates.
    public static let textFamilies: [String] = familiesMatching(fixedPitch: false)

    private static func familiesMatching(fixedPitch: Bool) -> [String] {
        NSFontManager.shared.availableFontFamilies.compactMap { family -> String? in
            // Some families have no usable members at size 12 (decorative
            // dingbat fonts, etc.); skip them rather than crash on picker.
            guard let font = NSFont(name: family, size: 12) else { return nil }
            return font.isFixedPitch == fixedPitch ? family : nil
        }
        .sorted { $0.localizedCaseInsensitiveCompare($1) == .orderedAscending }
    }
}
