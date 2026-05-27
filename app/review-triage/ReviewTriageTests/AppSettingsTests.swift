import Foundation
import Testing
@testable import ReviewTriage

@Suite("AppSettings")
struct AppSettingsTests {
    @Test func paletteDefaultsMatchSettingsDefaults() {
        let palette = FontPalette.standard
        #expect(palette.textSize == AppSettingsDefaults.textFontSize)
        #expect(palette.codeSize == AppSettingsDefaults.codeFontSize)
        #expect(palette.textFamily == AppSettingsDefaults.textFontFamily)
        #expect(palette.codeFamily == AppSettingsDefaults.codeFontFamily)
    }

    @Test func paletteSizesScaleProportionallyWithTextSize() {
        // The size offsets relative to the user-chosen base must stay constant
        // — the visual hierarchy (caption < body < heading < title) is what
        // keeps the UI legible at any base size the user picks in Settings.
        let small = FontPalette(textSize: 11, codeSize: 13, textFamily: "", codeFamily: "")
        let large = FontPalette(textSize: 20, codeSize: 13, textFamily: "", codeFamily: "")

        #expect(small.bodySize == 11)
        #expect(small.headingSize == 18)
        #expect(small.titleSize == 22)

        #expect(large.bodySize == 20)
        #expect(large.headingSize == 27)
        #expect(large.titleSize == 31)

        // The gap is invariant.
        #expect(large.headingSize - large.bodySize == small.headingSize - small.bodySize)
        #expect(large.titleSize - large.bodySize == small.titleSize - small.bodySize)
    }

    @Test func captionSizeFloorsAtTenPoints() {
        // Below textSize 12 the natural offset (textSize − 2) would give 10 or
        // less; the floor keeps captions legible without overlapping body.
        let tiny = FontPalette(textSize: AppSettingsDefaults.minFontSize, codeSize: 13, textFamily: "", codeFamily: "")
        #expect(tiny.captionSize >= 10)
    }

    @Test func fontCatalogPartitionsByPitch() {
        // Don't assert against specific family names (they vary by macOS
        // version); just confirm the partition is non-empty on both sides and
        // mutually exclusive.
        let mono = Set(FontCatalog.monospacedFamilies)
        let text = Set(FontCatalog.textFamilies)
        #expect(!mono.isEmpty)
        #expect(!text.isEmpty)
        #expect(mono.isDisjoint(with: text))
    }

    @Test func appSettingsKeysAreNamespaced() {
        // Settings keys live in UserDefaults shared with every Mac app on
        // disk — namespacing them under `review-triage.` prevents accidental
        // clashes with system or third-party keys.
        #expect(AppSettingsKeys.textFontSize.hasPrefix("review-triage."))
        #expect(AppSettingsKeys.codeFontSize.hasPrefix("review-triage."))
        #expect(AppSettingsKeys.textFontFamily.hasPrefix("review-triage."))
        #expect(AppSettingsKeys.codeFontFamily.hasPrefix("review-triage."))
    }
}
