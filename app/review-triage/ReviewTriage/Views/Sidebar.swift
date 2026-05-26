import SwiftUI
import AppKit

struct Sidebar: View {
    @Bindable var state: TriageState

    var body: some View {
        List(state.rows, selection: $state.selectedRowID) { row in
            rowView(for: row)
                .tag(row.id)
                .listRowSeparator(.hidden)
        }
        .listStyle(.sidebar)
        // Single-key TUI shortcuts. SwiftUI routes onKeyPress to the focused
        // view, so these only fire when the sidebar has focus — not while the
        // discuss textarea is being edited.
        .onKeyPress(KeyEquivalent("f")) { dispatch(.fix); return .handled }
        .onKeyPress(KeyEquivalent("s")) { dispatch(.skip); return .handled }
        .onKeyPress(KeyEquivalent("d")) {
            state.openDiscussForCurrentSelection(); return .handled
        }
        .onKeyPress(KeyEquivalent.tab) {
            if let id = state.selectedRowID {
                state.acceptSuggestion(forRowAt: id)
                return .handled
            }
            return .ignored
        }
        .onKeyPress(KeyEquivalent("g")) {
            state.cycleGroupBy(); return .handled
        }
        .onKeyPress(KeyEquivalent.return) {
            if let row = state.selectedRow(), case .submit = row.kind {
                state.attemptSubmit()
                return .handled
            }
            return .ignored
        }
        .overlay(alignment: .bottom) {
            if !state.footerMessage.isEmpty {
                Text(state.footerMessage)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 6)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(.thinMaterial)
            }
        }
    }

    @ViewBuilder
    private func rowView(for row: Row) -> some View {
        switch row.kind {
        case .header(let groupKey):
            HeaderRow(row: row, groupKey: groupKey, state: state)
        case .item(let findingIdx):
            ItemRow(
                finding: state.findings[findingIdx],
                action: state.actions[findingIdx],
                hideGroupSuffix: state.groupBy == .type
            )
        case .submit:
            SubmitRow(planSummary: state.planSummary)
        }
    }

    private func dispatch(_ action: Action) {
        guard let id = state.selectedRowID else { return }
        switch id {
        case .item(let findingIdx):
            state.apply(action, toFindingAtIndex: findingIdx)
        case .header:
            state.applyToGroup(action, headerKind: id)
        case .submit:
            break
        }
    }
}

private struct HeaderRow: View {
    let row: Row
    let groupKey: String
    let state: TriageState

    var body: some View {
        let indices = state.indices(forGroupHeader: row.kind)
        let breakdown = Tally.breakdown(indices: indices, actions: state.actions)
        HStack(spacing: 6) {
            Image(systemName: "chevron.down")
                .font(.caption2.weight(.semibold))
                .foregroundStyle(.tertiary)
            Text(groupKey)
                .font(.subheadline.weight(.semibold))
            Text("(\(indices.count))")
                .font(.subheadline)
                .foregroundStyle(.secondary)
                .monospacedDigit()
            if !breakdown.isEmpty {
                Text("·").foregroundStyle(.tertiary)
                Text(breakdown)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            Spacer(minLength: 0)
        }
        .padding(.vertical, 3)
        .contentShape(Rectangle())
    }
}

private struct ItemRow: View {
    let finding: Finding
    let action: Action?
    let hideGroupSuffix: Bool

    var body: some View {
        HStack(spacing: 6) {
            Text(label)
                .font(.caption.weight(.medium))
                .foregroundStyle(.secondary)
                .monospacedDigit()
            Text(finding.title)
                .lineLimit(1)
                .truncationMode(.tail)
            Spacer(minLength: 4)
            Text("·\(finding.score)")
                .font(.caption.monospacedDigit())
                .foregroundStyle(.secondary)
            BadgeView(action: action, suggestion: finding.selection)
        }
        .padding(.vertical, 2)
        .contentShape(Rectangle())
    }

    private var label: String {
        hideGroupSuffix ? agentLabelWithoutGroupSuffix : finding.agentLabel
    }

    private var agentLabelWithoutGroupSuffix: String {
        finding.agentLabel.replacingOccurrences(of: ": \(finding.group)", with: "")
    }
}

private struct SubmitRow: View {
    let planSummary: String

    var body: some View {
        HStack(spacing: 6) {
            Image(systemName: "checkmark.seal.fill")
                .foregroundStyle(.tint)
            Text("Submit")
                .font(.subheadline.weight(.semibold))
            Text(planSummary)
                .font(.caption.monospacedDigit())
                .foregroundStyle(.secondary)
            Spacer(minLength: 0)
        }
        .padding(.vertical, 4)
        .contentShape(Rectangle())
    }
}
