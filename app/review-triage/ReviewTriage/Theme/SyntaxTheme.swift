import SwiftUI
import AppKit

public enum SyntaxTheme {
    public static func color(for captureName: String) -> Color? {
        let parts = captureName.split(separator: ".").map(String.init)
        for i in stride(from: parts.count, through: 1, by: -1) {
            let prefix = parts.prefix(i).joined(separator: ".")
            if let color = palette[prefix] { return color }
        }
        return nil
    }

    private static let palette: [String: Color] = [
        "keyword":            Color(nsColor: .systemPink),
        "keyword.control":    Color(nsColor: .systemPink),
        "keyword.return":     Color(nsColor: .systemPink),
        "keyword.import":     Color(nsColor: .systemPink),
        "keyword.function":   Color(nsColor: .systemPink),
        "keyword.operator":   Color(nsColor: .systemPink),
        "operator":           Color(nsColor: .secondaryLabelColor),
        "punctuation":        Color(nsColor: .tertiaryLabelColor),
        "string":             Color(nsColor: .systemRed),
        "string.special":     Color(nsColor: .systemOrange),
        "character":          Color(nsColor: .systemRed),
        "number":             Color(nsColor: .systemOrange),
        "boolean":            Color(nsColor: .systemOrange),
        "constant":           Color(nsColor: .systemOrange),
        "constant.builtin":   Color(nsColor: .systemOrange),
        "comment":            Color(nsColor: .tertiaryLabelColor),
        "function":           Color(nsColor: .systemBlue),
        "function.builtin":   Color(nsColor: .systemBlue),
        "function.macro":     Color(nsColor: .systemTeal),
        "method":             Color(nsColor: .systemBlue),
        "type":               Color(nsColor: .systemTeal),
        "type.builtin":       Color(nsColor: .systemTeal),
        "constructor":        Color(nsColor: .systemTeal),
        "variable":           Color(nsColor: .labelColor),
        "variable.builtin":   Color(nsColor: .systemPurple),
        "variable.parameter": Color(nsColor: .labelColor),
        "property":           Color(nsColor: .systemPurple),
        "attribute":          Color(nsColor: .systemIndigo),
        "tag":                Color(nsColor: .systemBlue),
        "label":              Color(nsColor: .systemIndigo),
        "module":             Color(nsColor: .systemTeal),
        "namespace":          Color(nsColor: .systemTeal),
        "escape":             Color(nsColor: .systemOrange),
        "regex":              Color(nsColor: .systemRed),
        "embedded":           Color(nsColor: .labelColor),
    ]
}
