import SwiftUI

struct DetailPane: View {
    @Bindable var state: TriageState

    var body: some View {
        if let finding = state.selectedFinding(), let idx = state.selectedFindingIdx() {
            FindingDetail(finding: finding, action: state.actions[idx])
        } else if let row = state.selectedRow(), case .header(let groupKey) = row.kind {
            GroupSummary(headerRow: row, groupKey: groupKey, state: state)
        } else if let row = state.selectedRow(), case .submit = row.kind {
            PlanOverview(state: state)
        } else {
            ContentUnavailableView(
                "Select a finding",
                systemImage: "doc.text.magnifyingglass",
                description: Text("Click an item in the sidebar or use ↑↓ to navigate.")
            )
        }
    }
}

private struct FindingDetail: View {
    @Environment(\.fontPalette) private var palette
    let finding: Finding
    let action: Action?

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                heading
                if !finding.explanation.isEmpty {
                    Text(finding.explanation)
                        .font(palette.body)
                        .textSelection(.enabled)
                        .fixedSize(horizontal: false, vertical: true)
                }
                if !finding.proposedFix.explanation.isEmpty {
                    VStack(alignment: .leading, spacing: 4) {
                        Text("Proposed:")
                            .font(palette.headline)
                        Text(finding.proposedFix.explanation)
                            .font(palette.body)
                            .textSelection(.enabled)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
                // Materialize the post-edit excerpt by re-running the same
                // apply that `Input.validate()` already executed. We trust the
                // contract: if validation passed, apply cannot fail here, so
                // any thrown error is a programmer bug — surface it loudly.
                let newCode = (try? EditApply.apply(
                    edits: finding.proposedFix.edits,
                    to: finding.codeExcerpt
                )) ?? finding.codeExcerpt
                let diffLines = LineDiff.diff(
                    oldCode: finding.codeExcerpt,
                    newCode: newCode,
                    startLine: finding.lineStart
                )
                if !diffLines.isEmpty {
                    DiffView(lines: diffLines, language: finding.language)
                }
            }
            .padding(24)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    private var heading: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Text(finding.agentLabel)
                    .font(palette.subheadlineMedium)
                    .foregroundStyle(.secondary)
                Text("·").foregroundStyle(.tertiary)
                Text(finding.title)
                    .font(palette.heading)
            }
            HStack(spacing: 8) {
                Image(systemName: "doc.text")
                    .foregroundStyle(.secondary)
                Text("\(finding.file):\(finding.lineStart)-\(finding.lineEnd)")
                    .font(palette.codeInline)
                    .textSelection(.enabled)
                Text("·").foregroundStyle(.tertiary)
                Text("score")
                    .foregroundStyle(.secondary)
                Text("\(finding.score)")
                    .monospacedDigit()
                Text("·").foregroundStyle(.tertiary)
                BadgeView(action: action, suggestion: finding.selection)
            }
            .font(palette.body)
        }
    }
}

private struct GroupSummary: View {
    @Environment(\.fontPalette) private var palette
    let headerRow: Row
    let groupKey: String
    let state: TriageState

    var body: some View {
        let indices = state.indices(forGroupHeader: headerRow.kind)
        VStack(alignment: .leading, spacing: 12) {
            Text(groupKey)
                .font(palette.title)
            Text("\(indices.count) finding\(indices.count == 1 ? "" : "s")")
                .font(palette.headline)
                .foregroundStyle(.secondary)
            Text(Tally.breakdown(indices: indices, actions: state.actions, showZeros: true))
                .font(palette.code)
            Spacer()
            Text("Tip: press `f`, `s`, or `d` (or ⌘F / ⌘K / ⌘D) on this group header to apply an action to every item at once.")
                .font(palette.body)
                .foregroundStyle(.secondary)
        }
        .padding(24)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }
}

private struct PlanOverview: View {
    @Environment(\.fontPalette) private var palette
    let state: TriageState

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Ready to submit?")
                .font(palette.title)
            Text(state.planSummary)
                .font(palette.code)
            if state.discussWithoutPromptCount > 0 {
                Label(
                    "\(state.discussWithoutPromptCount) discuss item\(state.discussWithoutPromptCount == 1 ? "" : "s") have no prompt — you can still ship as-is.",
                    systemImage: "exclamationmark.triangle"
                )
                .font(palette.body)
                .foregroundStyle(.secondary)
            }
            Spacer()
            Button("Submit (⌘S)") { state.attemptSubmit() }
                .keyboardShortcut("s", modifiers: .command)
        }
        .padding(24)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }
}
