import Foundation
import Testing
@testable import ReviewTriage

@Suite("TriageState")
@MainActor
struct TriageStateTests {
    @Test func initialSelectionIsFirstItem() {
        let state = makeState(findingCount: 3)
        let row = state.selectedRow()
        #expect(row != nil)
        #expect(row?.kind.isItem == true)
    }

    @Test func applyOnItemAdvancesToNextItem() {
        let state = makeState(findingCount: 3)
        let firstID = state.selectedRowID
        state.apply(.fix, toFindingAtIndex: 0)
        #expect(state.actions[0] == .fix)
        #expect(state.selectedRowID != firstID)
        let row = state.selectedRow()
        #expect(row?.kind.isItem == true)
    }

    @Test func applyOnLastItemLandsOnSubmit() {
        let state = makeState(findingCount: 2)
        state.apply(.fix, toFindingAtIndex: 0)
        state.apply(.fix, toFindingAtIndex: 1)
        #expect(state.selectedRow()?.kind == .submit)
    }

    @Test func bulkActionOnHeaderAffectsEveryItemInGroup() {
        let state = makeState(findingCount: 3, group: "comments")
        let header = state.rows.first { $0.kind.isHeader }!
        state.applyToGroup(.skip, headerKind: header.kind)
        let allSkip = state.actions.allSatisfy { $0 == .skip }
        #expect(allSkip)
    }

    @Test func bulkDiscussOnHeaderSetsEmptyPromptsWithoutOpeningSheet() {
        let state = makeState(findingCount: 2, group: "comments")
        let header = state.rows.first { $0.kind.isHeader }!
        state.selectedRowID = header.id
        state.openDiscussForCurrentSelection()
        let allDiscuss = state.actions.allSatisfy { $0 == .discuss }
        let allPromptsEmpty = state.prompts.allSatisfy { $0.isEmpty }
        #expect(state.discussShowing == false)
        #expect(allDiscuss)
        #expect(allPromptsEmpty)
    }

    @Test func tabOnItemWithSuggestionAdoptsIt() {
        let state = makeState(findingCount: 2, selection: .fix)
        let firstItem = state.rows.first { $0.kind.isItem }!
        state.selectedRowID = firstItem.id
        state.acceptSuggestion(forRowAt: firstItem.id)
        #expect(state.actions[firstItem.findingIdx!] == .fix)
    }

    @Test func tabOnItemWithoutSuggestionSurfacesFooterMessage() {
        let state = makeState(findingCount: 1, selection: nil)
        let item = state.rows.first { $0.kind.isItem }!
        state.selectedRowID = item.id
        state.acceptSuggestion(forRowAt: item.id)
        #expect(state.actions[item.findingIdx!] == nil)
        #expect(state.footerMessage.contains("no suggestion"))
    }

    @Test func acceptSuggestionOnHeaderAppliesEachItemsOwnSuggestion() {
        // Three findings in one group, two with selections, one without.
        let findings = [
            makeFinding(id: 1, group: "comments", score: 50, selection: .fix),
            makeFinding(id: 2, group: "comments", score: 50, selection: nil),
            makeFinding(id: 3, group: "comments", score: 50, selection: .skip),
        ]
        let state = makeState(findings: findings)
        let header = state.rows.first { $0.kind.isHeader }!
        state.selectedRowID = header.id
        state.acceptSuggestion(forRowAt: header.id)
        // Items with a selection adopt it; nil-selection item stays nil.
        #expect(state.actions[0] == .fix)
        #expect(state.actions[1] == nil)
        #expect(state.actions[2] == .skip)
    }

    @Test func bulkDiscussOnHeaderResetsPromptsMatchingGoTUI() {
        // The Go TUI's bulk-discuss on a group header clears every prompt
        // back to empty (the user can then revisit individual items to fill
        // them in). This test pins that contract so a future refactor that
        // tries to preserve prompts has to consciously update both sides.
        let state = makeState(findingCount: 2, group: "comments")
        // Pre-seed a prompt on finding 0.
        state.openDiscuss(forFindingAtIndex: 0)
        state.discussDraft = "earlier note"
        state.saveDiscuss()
        // After saveDiscuss, selection has advanced; move it back to the header.
        let header = state.rows.first { $0.kind.isHeader }!
        state.selectedRowID = header.id
        // Now bulk-discuss on the header — every prompt gets reset to "".
        state.openDiscussForCurrentSelection()
        #expect(state.actions[0] == .discuss)
        #expect(state.actions[1] == .discuss)
        #expect(state.prompts[0] == "")
        #expect(state.prompts[1] == "")
    }

    @Test func cycleGroupByGoesThroughAllDimensions() {
        let state = makeState(findingCount: 1)
        #expect(state.groupBy == .type)
        state.cycleGroupBy()
        #expect(state.groupBy == .score)
        state.cycleGroupBy()
        #expect(state.groupBy == .action)
        state.cycleGroupBy()
        #expect(state.groupBy == .file)
        state.cycleGroupBy()
        #expect(state.groupBy == .type)
    }

    @Test func attemptSubmitWithUndecidedRefuses() {
        let state = makeState(findingCount: 2)
        state.apply(.fix, toFindingAtIndex: 0)
        state.attemptSubmit()
        #expect(state.submitConfirmShowing == false)
        #expect(state.footerMessage.contains("still undecided"))
    }

    @Test func attemptSubmitWithAllDecidedOpensConfirm() {
        let state = makeState(findingCount: 2)
        state.apply(.fix, toFindingAtIndex: 0)
        state.apply(.skip, toFindingAtIndex: 1)
        state.attemptSubmit()
        #expect(state.submitConfirmShowing == true)
    }

    @Test func discussSaveCommitsActionAndPrompt() {
        let state = makeState(findingCount: 1)
        state.openDiscuss(forFindingAtIndex: 0)
        state.discussDraft = "Why throw here?"
        state.saveDiscuss()
        #expect(state.actions[0] == .discuss)
        #expect(state.prompts[0] == "Why throw here?")
        #expect(state.discussShowing == false)
    }

    @Test func discussCancelOnFreshItemRevertsAction() {
        let state = makeState(findingCount: 1)
        state.openDiscuss(forFindingAtIndex: 0)
        #expect(state.discussWasFresh)
        state.cancelDiscuss()
        #expect(state.actions[0] == nil)
    }

    @Test func discussCancelOnPreExistingDiscussKeepsPrompt() {
        let state = makeState(findingCount: 1)
        state.openDiscuss(forFindingAtIndex: 0)
        state.discussDraft = "first take"
        state.saveDiscuss()
        state.openDiscuss(forFindingAtIndex: 0)
        #expect(state.discussWasFresh == false)
        state.discussDraft = "second take"
        state.cancelDiscuss()
        #expect(state.actions[0] == .discuss)
        #expect(state.prompts[0] == "first take") // unchanged
    }

    @Test func finalizeProducesOneDecisionPerDecidedFinding() {
        let state = makeState(findingCount: 3)
        state.apply(.fix, toFindingAtIndex: 0)
        state.apply(.skip, toFindingAtIndex: 1)
        state.openDiscuss(forFindingAtIndex: 2)
        state.discussDraft = "ask"
        state.saveDiscuss()
        let output = state.finalize()
        #expect(output.decisions.count == 3)
        #expect(output.decisions[0].action == .fix)
        #expect(output.decisions[0].discussPrompt == nil)
        #expect(output.decisions[2].action == .discuss)
        #expect(output.decisions[2].discussPrompt == "ask")
    }

    @Test func planSummaryIncludesAllBuckets() {
        let state = makeState(findingCount: 3)
        state.apply(.fix, toFindingAtIndex: 0)
        let summary = state.planSummary
        #expect(summary.contains("1 fix"))
        #expect(summary.contains("0 discuss"))
        #expect(summary.contains("0 skip"))
        #expect(summary.contains("2 ?"))
    }

    @Test func discussWithoutPromptIsCounted() {
        let state = makeState(findingCount: 2)
        state.openDiscuss(forFindingAtIndex: 0)
        state.discussDraft = "explained"
        state.saveDiscuss()
        state.openDiscuss(forFindingAtIndex: 1)
        state.saveDiscuss()
        #expect(state.discussWithoutPromptCount == 1)
    }

    private func makeState(findingCount: Int, group: String = "comments", selection: Action? = nil) -> TriageState {
        let findings = (0..<findingCount).map { i in
            Finding(
                id: i + 1, title: "T\(i)", group: group, agentLabel: "#\(i+1): \(group)",
                score: 50, explanation: "", file: "f.ts", lineStart: 1, lineEnd: 1,
                language: "typescript", codeExcerpt: "x",
                proposedFix: ProposedFix(explanation: "", edits: [Edit(find: "x", replace: "y")]),
                selection: selection
            )
        }
        return makeState(findings: findings)
    }

    private func makeState(findings: [Finding]) -> TriageState {
        let input = Input(schemaVersion: 1, branch: "b", mergeBase: "m", findings: findings)
        return TriageState(
            input: input,
            outputURL: FileManager.default.temporaryDirectory
                .appendingPathComponent("rt-test-\(UUID().uuidString).json")
        )
    }

    private func makeFinding(id: Int, group: String, score: Int, selection: Action?) -> Finding {
        Finding(
            id: id, title: "T\(id)", group: group, agentLabel: "#\(id): \(group)",
            score: score, explanation: "", file: "f.ts", lineStart: 1, lineEnd: 1,
            language: "typescript", codeExcerpt: "x",
            proposedFix: ProposedFix(explanation: "", edits: [Edit(find: "x", replace: "y")]),
            selection: selection
        )
    }
}

// MARK: - Test-only boolean predicates on RowKind
//
// `Row` already exposes `groupKey: String?` and `findingIdx: Int?`. We just add
// `isHeader/isItem/isSubmit` so test assertions can stay one-line.

extension RowKind {
    var isHeader: Bool {
        if case .header = self { return true } else { return false }
    }
    var isItem: Bool {
        if case .item = self { return true } else { return false }
    }
    var isSubmit: Bool {
        if case .submit = self { return true } else { return false }
    }
}
