import SwiftUI
import AppKit

public enum BadgeStyle {
    public static func color(action: Action?) -> Color {
        switch action {
        case .fix: return Color(nsColor: .systemGreen)
        case .skip: return Color(nsColor: .systemOrange)
        case .discuss: return Color(nsColor: .systemPurple)
        case .none: return Color(nsColor: .systemRed)
        }
    }

    public static func text(action: Action?, suggestion: Action? = nil) -> String {
        if let action {
            switch action {
            case .fix: return "FIX"
            case .skip: return "SKIP"
            case .discuss: return "DISCUSS"
            }
        }
        guard let suggestion else { return "?" }
        switch suggestion {
        case .fix: return "FIX?"
        case .skip: return "SKIP?"
        case .discuss: return "DISCUSS?"
        }
    }

    public static func symbol(action: Action?) -> String {
        switch action {
        case .fix: return "checkmark.circle.fill"
        case .skip: return "minus.circle.fill"
        case .discuss: return "bubble.left.and.bubble.right.fill"
        case .none: return "questionmark.circle.fill"
        }
    }
}
