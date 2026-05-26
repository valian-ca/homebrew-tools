import Foundation
import SwiftUI
import Testing
@testable import ReviewTriage

@Suite("AppTheme")
struct AppThemeTests {
    @Test func autoMapsToNilColorScheme() {
        #expect(AppTheme.auto.colorScheme == nil)
    }

    @Test func lightMapsToLightColorScheme() {
        #expect(AppTheme.light.colorScheme == .light)
    }

    @Test func darkMapsToDarkColorScheme() {
        #expect(AppTheme.dark.colorScheme == .dark)
    }

    @Test func rawValuesAreStableForUserDefaultsPersistence() {
        #expect(AppTheme.auto.rawValue == "auto")
        #expect(AppTheme.light.rawValue == "light")
        #expect(AppTheme.dark.rawValue == "dark")
    }

    @Test func allCasesAreThreeMembers() {
        let cases = AppTheme.allCases
        #expect(cases.count == 3)
        #expect(cases.contains(.auto))
        #expect(cases.contains(.light))
        #expect(cases.contains(.dark))
    }

    @Test func defaultsKeyIsStable() {
        // Pinned because changing it would orphan every existing user's preference.
        #expect(AppTheme.defaultsKey == "appTheme")
    }
}
