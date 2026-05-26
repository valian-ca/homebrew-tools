import SwiftUI
import AppKit

// Approximate width of one monospaced digit at .callout — derived empirically.
// Replace with `measure` if Dynamic Type support is added later.
private let approximateDigitWidthPoints: CGFloat = 9

extension DiffLine: Identifiable {
    public var id: String { "\(kind)-\(oldNum)-\(newNum)-\(text.hashValue)" }
}

struct DiffView: View {
    let lines: [DiffLine]
    let language: String

    var body: some View {
        let maxNum = lines.map { max($0.oldNum, $0.newNum) }.max() ?? 0
        let numWidth = max(2, String(maxNum).count)
        VStack(alignment: .leading, spacing: 0) {
            ForEach(lines) { line in
                DiffLineRow(
                    line: line,
                    language: language,
                    numColumnWidth: CGFloat(numWidth) * approximateDigitWidthPoints
                )
            }
        }
        .font(.system(.callout, design: .monospaced))
        .clipShape(RoundedRectangle(cornerRadius: 6))
        .overlay(
            RoundedRectangle(cornerRadius: 6)
                .stroke(Color(nsColor: .separatorColor), lineWidth: 1)
        )
    }
}

private struct DiffLineRow: View {
    let line: DiffLine
    let language: String
    let numColumnWidth: CGFloat

    var body: some View {
        HStack(spacing: 0) {
            gutter
            content
        }
    }

    private var gutter: some View {
        HStack(spacing: 6) {
            Text(line.oldNum > 0 ? String(line.oldNum) : "")
                .frame(width: numColumnWidth, alignment: .trailing)
            Text(line.newNum > 0 ? String(line.newNum) : "")
                .frame(width: numColumnWidth, alignment: .trailing)
        }
        .font(.caption.monospacedDigit())
        .foregroundStyle(DiffColors.gutterForeground)
        .padding(.horizontal, 8)
        .padding(.vertical, 1)
        .background(DiffColors.gutterBackground)
    }

    private var content: some View {
        Text(attributedContent)
            .textSelection(.enabled)
            .padding(.horizontal, 8)
            .padding(.vertical, 1)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(DiffColors.background(kind: line.kind, emphasis: false))
    }

    private var attributedContent: AttributedString {
        var attr = SyntaxHighlight.highlight(line.text, language: language)
        let emphBg = DiffColors.background(kind: line.kind, emphasis: true)
        for range in line.emphasisRanges {
            // Translate the `Range<String.Index>` (over `line.text`) into the
            // equivalent `Range<AttributedString.Index>` via character offsets.
            // Both index spaces share Character boundaries since `attr` was
            // built directly from `line.text`.
            let startOffset = line.text.distance(from: line.text.startIndex, to: range.lowerBound)
            let endOffset = line.text.distance(from: line.text.startIndex, to: range.upperBound)
            let start = attr.index(attr.startIndex, offsetByCharacters: startOffset)
            let end = attr.index(attr.startIndex, offsetByCharacters: endOffset)
            attr[start..<end].backgroundColor = emphBg
        }
        return attr
    }
}
