import SwiftUI
import AppKit

struct Sidebar: View {
    @Environment(\.fontPalette) private var palette
    @Bindable var state: TriageState

    var body: some View {
        // The review title lives in the macOS title bar (set via
        // `.navigationTitle(state.inputTitle)` in `ReviewTriageApp`). The
        // sidebar deliberately doesn't repeat it — duplicating the same
        // string a few pixels apart was just visual noise.
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
                    .font(palette.caption)
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
                action: state.actions[findingIdx]
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
    @Environment(\.fontPalette) private var palette
    let row: Row
    let groupKey: String
    let state: TriageState

    var body: some View {
        let indices = state.indices(forGroupHeader: row.kind)
        let breakdown = Tally.breakdown(indices: indices, actions: state.actions)
        HStack(spacing: 6) {
            Image(systemName: "chevron.down")
                .font(palette.captionSemibold)
                .foregroundStyle(.tertiary)
            Text(groupKey)
                .font(palette.subheadlineSemibold)
            Text("(\(indices.count))")
                .font(palette.subheadline)
                .foregroundStyle(.secondary)
                .monospacedDigit()
            if !breakdown.isEmpty {
                Text("·").foregroundStyle(.tertiary)
                Text(breakdown)
                    .font(palette.caption)
                    .foregroundStyle(.secondary)
            }
            Spacer(minLength: 0)
        }
        .padding(.vertical, 3)
        .contentShape(Rectangle())
    }
}

private struct ItemRow: View {
    @Environment(\.fontPalette) private var palette
    let finding: Finding
    let action: Action?

    var body: some View {
        HStack(spacing: 6) {
            // `finding.id` is the positional row number from the review table
            // (the `#` column in the All-issues output). Using it here matches
            // how the developer refers to a finding in chat ("the #4")
            // — and unlike the agent label, it is unique across the sidebar.
            Text("#\(finding.id)")
                .font(palette.captionMedium)
                .foregroundStyle(.secondary)
                .monospacedDigit()
            Text(finding.title)
                .font(palette.body)
                .lineLimit(1)
                .truncationMode(.tail)
            Spacer(minLength: 4)
            Text("·\(finding.score)")
                .font(palette.captionMonoDigits)
                .foregroundStyle(.secondary)
            BadgeView(action: action, suggestion: finding.selection)
        }
        // Indent items under their group header so the hierarchy reads at a
        // glance — the header carries the chevron and group name at the
        // default indent, items sit shifted right.
        .padding(.leading, 16)
        .padding(.vertical, 2)
        .contentShape(Rectangle())
    }
}

private struct SubmitRow: View {
    @Environment(\.fontPalette) private var palette
    let planSummary: String

    var body: some View {
        HStack(spacing: 6) {
            Image(systemName: "checkmark.seal.fill")
                .foregroundStyle(.tint)
            Text("Submit")
                .font(palette.subheadlineSemibold)
            Text(planSummary)
                .font(palette.captionMonoDigits)
                .foregroundStyle(.secondary)
            Spacer(minLength: 0)
        }
        .padding(.vertical, 4)
        .contentShape(Rectangle())
    }
}
