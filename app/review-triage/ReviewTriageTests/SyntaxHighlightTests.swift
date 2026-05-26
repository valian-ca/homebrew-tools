import Foundation
import Testing
@testable import ReviewTriage

@Suite("SyntaxHighlight")
struct SyntaxHighlightTests {
    @Test func unsupportedLanguageReturnsPlainText() {
        let result = SyntaxHighlight.highlight("let x = 1", language: "rust")
        #expect(result.runs.count == 1) // single uncoloured run
    }

    @Test func emptyTextReturnsEmptyAttributedString() {
        let result = SyntaxHighlight.highlight("", language: "typescript")
        let characterCount = result.characters.count
        #expect(characterCount == 0)
    }

    @Test func typescriptSnippetGetsHighlights() {
        let snippet = "const x: number = 42"
        let result = SyntaxHighlight.highlight(snippet, language: "typescript")
        let colouredRuns = result.runs.filter { $0.foregroundColor != nil }
        #expect(colouredRuns.count > 0, "TypeScript highlights query produced no token colors")
    }

    @Test func tsxSnippetGetsHighlights() {
        let snippet = "const App = () => <div>{foo}</div>"
        let result = SyntaxHighlight.highlight(snippet, language: "tsx")
        let colouredRuns = result.runs.filter { $0.foregroundColor != nil }
        #expect(colouredRuns.count > 0, "TSX highlights query produced no token colors")
    }

    @Test func goSnippetGetsHighlights() {
        let snippet = "package main\nfunc main() { fmt.Println(\"hi\") }"
        let result = SyntaxHighlight.highlight(snippet, language: "go")
        let colouredRuns = result.runs.filter { $0.foregroundColor != nil }
        #expect(colouredRuns.count > 0, "Go highlights query produced no token colors")
    }

    @Test func javaSnippetGetsHighlights() {
        let snippet = "public class Hello { public static void main(String[] a) {} }"
        let result = SyntaxHighlight.highlight(snippet, language: "java")
        let colouredRuns = result.runs.filter { $0.foregroundColor != nil }
        #expect(colouredRuns.count > 0, "Java highlights query produced no token colors")
    }

    @Test func kotlinSnippetGetsHighlights() {
        let snippet = "fun greet(name: String): String { return \"hi $name\" }"
        let result = SyntaxHighlight.highlight(snippet, language: "kotlin")
        let colouredRuns = result.runs.filter { $0.foregroundColor != nil }
        #expect(colouredRuns.count > 0, "Kotlin highlights query produced no token colors")
    }

    @Test func tsAliasResolvesToTypescript() {
        let ts = SyntaxHighlight.highlight("const x = 1", language: "TS")
        let tsColored = ts.runs.filter { $0.foregroundColor != nil }.count
        #expect(tsColored > 0)
    }

    @Test func ktAliasResolvesToKotlin() {
        let kt = SyntaxHighlight.highlight("val x = 1", language: "kt")
        let ktColored = kt.runs.filter { $0.foregroundColor != nil }.count
        #expect(ktColored > 0)
    }
}
