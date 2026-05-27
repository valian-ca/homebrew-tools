import SwiftUI
import AppKit

struct Sidebar: View {
    @Environment(\.fontPalette) private var palette
    @Bindable var state: TriageState

    var body: some View {
        VStack(spacing: 0) {
            if !state.inputTitle.isEmpty {
                titleBanner
            }
            list
        }
    }

    private var titleBanner: some View {
        Text(state.inputTitle)
            .font(palette.subheadlineSemibold)
            .foregroundStyle(.secondary)
            .lineLimit(2)
            .multilineTextAlignment(.leading)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 12)
            .padding(.vertical, 10)
            .background(Color(nsColor: .underPageBackgroundColor))
            .overlay(alignment: .bottom) {
                Divider()
            }
    }

    private var list: some View {
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
    let hideGroupSuffix: Bool

    var body: some View {
        HStack(spacing: 6) {
            Text(label)
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
