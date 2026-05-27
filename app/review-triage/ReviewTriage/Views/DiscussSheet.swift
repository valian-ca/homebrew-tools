import SwiftUI

struct DiscussSheet: View {
    private static let maxExcerptPreviewLines = 5

    @Environment(\.fontPalette) private var palette
    @Bindable var state: TriageState
    @FocusState private var textareaFocused: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            if let idx = state.discussingFindingIdx {
                header(for: state.findings[idx])
            }
            TextEditor(text: $state.discussDraft)
                .font(palette.body)
                .scrollContentBackground(.hidden)
                .padding(8)
                .frame(minHeight: 140)
                .background(Color(nsColor: .textBackgroundColor),
                            in: RoundedRectangle(cornerRadius: 6))
                .overlay(
                    RoundedRectangle(cornerRadius: 6)
                        .stroke(Color(nsColor: .separatorColor), lineWidth: 1)
                )
                .focused($textareaFocused)
                .onAppear { textareaFocused = true }
            HStack {
                Spacer()
                Button("Cancel", role: .cancel) {
                    state.cancelDiscuss()
                }
                .keyboardShortcut(.cancelAction)
                Button("Save") {
                    state.saveDiscuss()
                }
                .keyboardShortcut(.defaultAction)
            }
        }
        .padding(24)
        .frame(minWidth: 520, minHeight: 320)
    }

    private func header(for finding: Finding) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(finding.agentLabel)
                .font(palette.captionMedium)
                .foregroundStyle(.secondary)
            Text(finding.title)
                .font(palette.headline)
            Text("\(finding.file):\(finding.lineStart)-\(finding.lineEnd)")
                .font(palette.codeCaption)
                .foregroundStyle(.secondary)
                .textSelection(.enabled)
            if !finding.codeExcerpt.isEmpty {
                Text(compactExcerpt(finding.codeExcerpt))
                    .font(palette.codeInline)
                    .foregroundStyle(.secondary)
                    .padding(8)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(Color(nsColor: .quaternaryLabelColor).opacity(0.4),
                                in: RoundedRectangle(cornerRadius: 4))
            }
        }
    }

    private func compactExcerpt(_ excerpt: String) -> String {
        let maxLines = Self.maxExcerptPreviewLines
        let lines = excerpt.split(separator: "\n", maxSplits: maxLines, omittingEmptySubsequences: false)
        if lines.count > maxLines {
            return lines.prefix(maxLines).joined(separator: "\n") + "\n…"
        }
        return excerpt
    }
}
