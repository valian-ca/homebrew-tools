import Foundation
import Observation

@Observable
@MainActor
public final class TriageState {
    public let findings: [Finding]
    public let outputURL: URL

    public private(set) var actions: [Action?]
    public private(set) var prompts: [String]

    public var groupBy: GroupBy = .type {
        didSet {
            invalidateRowCache()
            // After regrouping, land on the first item of the new layout so the
            // detail pane has something coherent to show.
            selectedRowID = rows.first(where: {
                if case .item = $0.kind { return true } else { return false }
            })?.id
        }
    }

    public var selectedRowID: RowKind?
    public var footerMessage: String = ""

    public var quitConfirmShowing: Bool = false
    public var submitConfirmShowing: Bool = false
    public var discussShowing: Bool = false

    // Discuss sheet ephemeral state — the draft lives here so the textarea
    // edits don't mutate `prompts[i]` until the user saves.
    public var discussingFindingIdx: Int?
    public var discussDraft: String = ""
    /// `true` when `openDiscuss` flipped an item's action to `.discuss` for the
    /// first time. Cancelling the sheet then rolls the action back to its prior
    /// value (`nil`, `.fix`, `.skip` — whatever it was). When the item was
    /// already `.discuss`, cancel just leaves the prompt unchanged.
    public var discussWasFresh: Bool = false
    public var discussPreviousAction: Action?

    private var cachedRows: [Row]?
    private var rowIndexByKind: [RowKind: Int]?

    public var rows: [Row] {
        if let cachedRows { return cachedRows }
        let built = RowBuilder.buildRows(groupBy: groupBy, findings: findings, actions: actions)
        cachedRows = built
        var index: [RowKind: Int] = [:]
        for (i, row) in built.enumerated() {
            index[row.kind] = i
        }
        rowIndexByKind = index
        return built
    }

    private func invalidateRowCache() {
        cachedRows = nil
        rowIndexByKind = nil
    }

    private func rowIndex(for kind: RowKind) -> Int? {
        _ = rows  // trigger lazy build
        return rowIndexByKind?[kind]
    }

    public init(input: Input, outputURL: URL) {
        self.findings = input.findings
        self.outputURL = outputURL
        self.actions = Array(repeating: nil, count: input.findings.count)
        self.prompts = Array(repeating: "", count: input.findings.count)
        // Initial selection: the first actual finding row (skip the leading header).
        self.selectedRowID = RowBuilder.buildRows(
            groupBy: groupBy, findings: input.findings, actions: actions
        ).first(where: {
            if case .item = $0.kind { return true } else { return false }
        })?.id
    }

    public func apply(_ action: Action, toFindingAtIndex idx: Int) {
        actions[idx] = action
        invalidateRowCache()
        footerMessage = ""
        advanceToNextItem()
    }

    public func applyToGroup(_ action: Action, headerKind: RowKind) {
        guard case .header = headerKind else { return }
        for idx in indices(forGroupHeader: headerKind) {
            actions[idx] = action
            if action != .discuss {
                prompts[idx] = ""
            }
        }
        invalidateRowCache()
        footerMessage = ""
        advanceToNextHeader(from: headerKind)
    }

    public func acceptSuggestion(forRowAt kind: RowKind) {
        switch kind {
        case .item(let findingIdx):
            guard let suggestion = findings[findingIdx].selection else {
                footerMessage = "no suggestion to accept on this finding"
                return
            }
            apply(suggestion, toFindingAtIndex: findingIdx)
        case .header:
            // Header Tab applies each item's own suggestion; items without one
            // are silently left undecided — same as the Go TUI's bulk Tab.
            for idx in indices(forGroupHeader: kind) {
                if let suggestion = findings[idx].selection {
                    actions[idx] = suggestion
                }
            }
            invalidateRowCache()
            footerMessage = ""
            advanceToNextHeader(from: kind)
        case .submit:
            break
        }
    }

    public func cycleGroupBy() {
        groupBy = groupBy.next()
    }

    public func openDiscuss(forFindingAtIndex idx: Int) {
        discussingFindingIdx = idx
        discussDraft = prompts[idx]
        discussPreviousAction = actions[idx]
        discussWasFresh = actions[idx] != .discuss
        discussShowing = true
    }

    public func openDiscussForCurrentSelection() {
        guard let id = selectedRowID else { return }
        switch id {
        case .item(let findingIdx):
            openDiscuss(forFindingAtIndex: findingIdx)
        case .header:
            // Bulk discuss on a group header: no sheet, all items become
            // .discuss with an empty prompt. The user can revisit individual
            // items to fill prompts later. Matches Go TUI behaviour.
            for idx in indices(forGroupHeader: id) {
                actions[idx] = .discuss
                prompts[idx] = ""
            }
            invalidateRowCache()
            footerMessage = ""
            advanceToNextHeader(from: id)
        case .submit:
            break
        }
    }

    public func saveDiscuss() {
        guard let idx = discussingFindingIdx else { return }
        actions[idx] = .discuss
        prompts[idx] = discussDraft
        invalidateRowCache()
        footerMessage = ""
        closeDiscussSheet()
        advanceToNextItem()
    }

    public func cancelDiscuss() {
        guard let idx = discussingFindingIdx else { return }
        // When the sheet was opened by a fresh `d` keystroke, revert the action
        // so cancel feels like undo. When the item was already `.discuss`,
        // keep the prior prompt unchanged.
        if discussWasFresh {
            actions[idx] = discussPreviousAction
            invalidateRowCache()
        }
        closeDiscussSheet()
    }

    private func closeDiscussSheet() {
        discussShowing = false
        discussingFindingIdx = nil
        discussDraft = ""
        discussWasFresh = false
        discussPreviousAction = nil
    }

    public func attemptSubmit() {
        let undecided = actions.filter { $0 == nil }.count
        if undecided > 0 {
            footerMessage = "\(undecided) finding(s) still undecided — decide all before submitting"
            return
        }
        footerMessage = ""
        submitConfirmShowing = true
    }

    public func requestQuit() {
        quitConfirmShowing = true
    }

    public func finalize() -> Output {
        var decisions: [Decision] = []
        decisions.reserveCapacity(findings.count)
        for (i, finding) in findings.enumerated() {
            guard let action = actions[i] else {
                preconditionFailure("finalize must not see undecided findings; gated by attemptSubmit")
            }
            let prompt: String? = action == .discuss ? prompts[i] : nil
            decisions.append(Decision(id: finding.id, action: action, discussPrompt: prompt))
        }
        return Output(decisions: decisions)
    }

    /// Number of discuss items whose prompt is empty — surfaced by the Confirm
    /// sheet as a non-blocking warning (the contract allows empty prompts).
    public var discussWithoutPromptCount: Int {
        var n = 0
        for (i, action) in actions.enumerated() where action == .discuss {
            if prompts[i].isEmpty { n += 1 }
        }
        return n
    }

    public var planSummary: String {
        Tally.breakdown(
            indices: Array(findings.indices),
            actions: actions,
            showZeros: true
        )
    }

    public func selectedRow() -> Row? {
        guard let id = selectedRowID, let idx = rowIndex(for: id) else { return nil }
        return rows[idx]
    }

    public func selectedFinding() -> Finding? {
        guard let id = selectedRowID, case .item(let findingIdx) = id else { return nil }
        return findings[findingIdx]
    }

    public func selectedFindingIdx() -> Int? {
        guard let id = selectedRowID, case .item(let findingIdx) = id else { return nil }
        return findingIdx
    }

    public func advanceSelectionToNextFinding() {
        advanceToNextItem()
    }

    public func retreatSelectionToPreviousFinding() {
        guard let id = selectedRowID,
              let currentIdx = rowIndex(for: id) else { return }
        let currentRows = rows
        for i in stride(from: currentIdx - 1, through: 0, by: -1) {
            if case .item = currentRows[i].kind {
                selectedRowID = currentRows[i].id
                return
            }
        }
    }

    private func advanceToNextItem() {
        let currentRows = rows
        guard let id = selectedRowID,
              let currentIdx = rowIndex(for: id) else { return }
        for i in (currentIdx + 1)..<currentRows.count {
            if case .item = currentRows[i].kind {
                selectedRowID = currentRows[i].id
                return
            }
        }
        // No more items — land on the submit row so the developer can press
        // Enter to attempt submission without extra navigation.
        if let submit = currentRows.last(where: {
            if case .submit = $0.kind { return true } else { return false }
        }) {
            selectedRowID = submit.id
        }
    }

    private func advanceToNextHeader(from kind: RowKind) {
        let currentRows = rows
        guard let currentIdx = rowIndex(for: kind) else { return }
        for i in (currentIdx + 1)..<currentRows.count {
            switch currentRows[i].kind {
            case .header, .submit:
                selectedRowID = currentRows[i].id
                return
            case .item:
                continue
            }
        }
    }

    /// Returns the finding indices owned by a header row (everything between
    /// the header and the next header/submit). Returns `[]` for non-header
    /// kinds. Single source of truth — `Sidebar.HeaderRow` and
    /// `DetailPane.GroupSummary` both delegate here.
    public func indices(forGroupHeader kind: RowKind) -> [Int] {
        guard case .header = kind,
              let headerIdx = rowIndex(for: kind) else { return [] }
        let currentRows = rows
        var indices: [Int] = []
        for i in (headerIdx + 1)..<currentRows.count {
            guard case .item(let findingIdx) = currentRows[i].kind else { break }
            indices.append(findingIdx)
        }
        return indices
    }
}
