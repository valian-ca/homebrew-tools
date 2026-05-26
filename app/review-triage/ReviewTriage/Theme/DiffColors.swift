import SwiftUI
import AppKit

public enum DiffColors {
    public static func background(kind: DiffLine.Kind, emphasis: Bool) -> Color {
        let base: Color
        switch kind {
        case .add: base = Color(nsColor: .systemGreen)
        case .delete: base = Color(nsColor: .systemRed)
        case .context: return .clear
        }
        return base.opacity(emphasis ? 0.40 : 0.15)
    }

    public static var gutterForeground: Color {
        Color(nsColor: .tertiaryLabelColor)
    }

    public static var gutterBackground: Color {
        Color(nsColor: .controlBackgroundColor)
    }
}
