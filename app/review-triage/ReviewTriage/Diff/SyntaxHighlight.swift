import Foundation
import SwiftUI
import SwiftTreeSitter
import TreeSitterTypeScript
import TreeSitterTSX
import TreeSitterJava
import TreeSitterGo
import TreeSitterKotlin

public enum SyntaxHighlight {
    public static let v1BaselineLanguages: Set<String> = [
        "typescript", "ts", "tsx",
        "java",
        "kotlin", "kt", "kts",
        "go",
    ]

    public static func isSupported(_ language: String) -> Bool {
        v1BaselineLanguages.contains(language.lowercased())
    }

    public static func highlight(_ text: String, language: String) -> AttributedString {
        guard let parser = resolveParser(for: language) else {
            return AttributedString(text)
        }
        do {
            return try applyHighlights(text: text, parser: parser)
        } catch {
            // A tokenisation failure on one snippet shouldn't blow up the
            // whole UI — fall through to uncoloured text and keep rendering.
            return AttributedString(text)
        }
    }

    private struct ResolvedParser {
        let language: Language
        let queryData: Data
    }

    private static func resolveParser(for languageName: String) -> ResolvedParser? {
        switch languageName.lowercased() {
        case "typescript", "ts":
            return parser(language: Language(language: tree_sitter_typescript()), bundle: "TreeSitterTypeScript_TreeSitterTypeScript")
        case "tsx":
            return parser(language: Language(language: tree_sitter_tsx()), bundle: "TreeSitterTypeScript_TreeSitterTSX")
        case "java":
            return parser(language: Language(language: tree_sitter_java()), bundle: "TreeSitterJava_TreeSitterJava")
        case "kotlin", "kt", "kts":
            return parser(language: Language(language: tree_sitter_kotlin()), bundle: "TreeSitterKotlin_TreeSitterKotlin")
        case "go":
            return parser(language: Language(language: tree_sitter_go()), bundle: "TreeSitterGo_TreeSitterGo")
        default:
            return nil
        }
    }

    private static func parser(language: Language, bundle bundleName: String) -> ResolvedParser? {
        // SwiftPM nests resource bundles two levels deep: <host>/Contents/Resources/<bundleName>.bundle/Contents/Resources/queries/.
        let hosts: [Bundle] = [.main, Bundle(for: BundleAnchor.self)]
        for host in hosts {
            guard let hostResources = host.resourceURL else { continue }
            let candidate = hostResources.appendingPathComponent("\(bundleName).bundle")
            guard FileManager.default.fileExists(atPath: candidate.path),
                  let bundle = Bundle(url: candidate) else { continue }
            if let url = bundle.url(forResource: "highlights", withExtension: "scm", subdirectory: "queries"),
               let data = try? Data(contentsOf: url) {
                return ResolvedParser(language: language, queryData: data)
            }
        }
        return nil
    }

    private final class BundleAnchor {}

    private static func applyHighlights(text: String, parser resolved: ResolvedParser) throws -> AttributedString {
        let parser = Parser()
        try parser.setLanguage(resolved.language)
        guard let tree = parser.parse(text) else { return AttributedString(text) }
        guard let rootNode = tree.rootNode else { return AttributedString(text) }

        let query = try Query(language: resolved.language, data: resolved.queryData)
        let cursor = query.execute(node: rootNode, in: tree)

        var attr = AttributedString(text)

        let nsText = text as NSString
        let nsTextLength = nsText.length

        for match in cursor {
            for capture in match.captures {
                let captureName = capture.name ?? ""
                guard let color = SyntaxTheme.color(for: captureName) else { continue }
                let nsRange = capture.range
                guard nsRange.location >= 0,
                      nsRange.length > 0,
                      nsRange.location + nsRange.length <= nsTextLength else { continue }
                guard let stringRange = Range(nsRange, in: text) else { continue }
                let startOffset = text.distance(from: text.startIndex, to: stringRange.lowerBound)
                let endOffset = text.distance(from: text.startIndex, to: stringRange.upperBound)
                let attrStart = attr.index(attr.startIndex, offsetByCharacters: startOffset)
                let attrEnd = attr.index(attr.startIndex, offsetByCharacters: endOffset)
                attr[attrStart..<attrEnd].foregroundColor = color
            }
        }
        return attr
    }
}
