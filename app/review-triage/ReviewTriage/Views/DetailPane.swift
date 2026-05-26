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
    let finding: Finding
    let action: Action?

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                heading
                if !finding.explanation.isEmpty {
                    Text(finding.explanation)
                        .textSelection(.enabled)
                        .fixedSize(horizontal: false, vertical: true)
                }
                if !finding.proposedFix.explanation.isEmpty {
                    VStack(alignment: .leading, spacing: 4) {
                        Text("Proposed:")
                            .font(.headline)
                        Text(finding.proposedFix.explanation)
                            .textSelection(.enabled)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
                let diffLines = LineDiff.diff(
                    oldCode: finding.codeExcerpt,
                    newCode: finding.proposedFix.code,
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
                    .font(.subheadline.weight(.medium))
                    .foregroundStyle(.secondary)
                Text("·").foregroundStyle(.tertiary)
                Text(finding.title)
                    .font(.title3.weight(.semibold))
            }
            HStack(spacing: 8) {
                Image(systemName: "doc.text")
                    .foregroundStyle(.secondary)
                Text("\(finding.file):\(finding.lineStart)-\(finding.lineEnd)")
                    .font(.callout.monospaced())
                    .textSelection(.enabled)
                Text("·").foregroundStyle(.tertiary)
                Text("score")
                    .foregroundStyle(.secondary)
                Text("\(finding.score)")
                    .monospacedDigit()
                Text("·").foregroundStyle(.tertiary)
                BadgeView(action: action, suggestion: finding.selection)
            }
            .font(.callout)
        }
    }
}

private struct GroupSummary: View {
    let headerRow: Row
    let groupKey: String
    let state: TriageState

    var body: some View {
        let indices = state.indices(forGroupHeader: headerRow.kind)
        VStack(alignment: .leading, spacing: 12) {
            Text(groupKey)
                .font(.title.weight(.semibold))
            Text("\(indices.count) finding\(indices.count == 1 ? "" : "s")")
                .font(.headline)
                .foregroundStyle(.secondary)
            Text(Tally.breakdown(indices: indices, actions: state.actions, showZeros: true))
                .font(.body.monospaced())
            Spacer()
            Text("Tip: press `f`, `s`, or `d` (or ⌘F / ⌘K / ⌘D) on this group header to apply an action to every item at once.")
                .font(.callout)
                .foregroundStyle(.secondary)
        }
        .padding(24)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }
}

private struct PlanOverview: View {
    let state: TriageState

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Ready to submit?")
                .font(.title.weight(.semibold))
            Text(state.planSummary)
                .font(.body.monospaced())
            if state.discussWithoutPromptCount > 0 {
                Label(
                    "\(state.discussWithoutPromptCount) discuss item\(state.discussWithoutPromptCount == 1 ? "" : "s") have no prompt — you can still ship as-is.",
                    systemImage: "exclamationmark.triangle"
                )
                .font(.callout)
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
