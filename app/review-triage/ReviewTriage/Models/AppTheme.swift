import SwiftUI

public enum AppTheme: String, CaseIterable, Sendable, Identifiable {
    case auto
    case light
    case dark

    public static let defaultsKey = "appTheme"

    public var id: String { rawValue }

    public var label: String {
        switch self {
        case .auto: return "Auto"
        case .light: return "Light"
        case .dark: return "Dark"
        }
    }

    public var colorScheme: ColorScheme? {
        switch self {
        case .auto: return nil
        case .light: return .light
        case .dark: return .dark
        }
    }
}
