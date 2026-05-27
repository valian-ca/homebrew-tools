import Foundation
import Testing
@testable import ReviewTriage

@Suite("GroupingLogic")
struct GroupingLogicTests {
    @Test func scoreBucketsMatchTUIBands() {
        #expect(ScoreBucket.label(for: 100) == "100")
        #expect(ScoreBucket.label(for: 99) == "<100")
        #expect(ScoreBucket.label(for: 76) == "<100")
        #expect(ScoreBucket.label(for: 75) == "≤75")
        #expect(ScoreBucket.label(for: 51) == "≤75")
        #expect(ScoreBucket.label(for: 50) == "≤50")
        #expect(ScoreBucket.label(for: 26) == "≤50")
        #expect(ScoreBucket.label(for: 25) == "≤25")
        #expect(ScoreBucket.label(for: 1) == "≤25")
        #expect(ScoreBucket.label(for: 0) == "0")
    }

    @Test func groupByCyclesThroughAllDimensions() {
        var dim = GroupBy.type
        let seen = (0..<4).map { _ -> GroupBy in
            let current = dim
            dim = dim.next()
            return current
        }
        #expect(seen == [.type, .score, .action, .file])
        #expect(dim == .type) // wraps around
    }

    @Test func buildRowsGroupsAndSortsByScore() {
        let findings = [
            makeFinding(id: 1, group: "comments", score: 30),
            makeFinding(id: 2, group: "comments", score: 90),
            makeFinding(id: 3, group: "bugs", score: 60),
        ]
        let actions: [Action?] = [nil, nil, nil]
        let rows = RowBuilder.buildRows(groupBy: .type, findings: findings, actions: actions)
        #expect(rows.count == 6)
        #expect(rows[0].kind == .header("comments"))
        #expect(rows[1].kind == .item(1)) // id 2 → idx 1
        #expect(rows[2].kind == .item(0)) // id 1 → idx 0
        #expect(rows[3].kind == .header("bugs"))
        #expect(rows[4].kind == .item(2))
        #expect(rows[5].kind == .submit)
    }

    @Test func scoreGroupingUsesCanonicalOrder() {
        let findings = [
            makeFinding(id: 1, group: "g", score: 25),  // ≤25
            makeFinding(id: 2, group: "g", score: 100), // 100
            makeFinding(id: 3, group: "g", score: 60),  // ≤75
        ]
        let rows = RowBuilder.buildRows(groupBy: .score, findings: findings, actions: [nil, nil, nil])
        let headerKeys = rows.compactMap(\.groupKey)
        #expect(headerKeys == ["100", "≤75", "≤25"])
    }

    @Test func actionGroupingPutsUndecidedUnderQuestionMark() {
        let findings = [
            makeFinding(id: 1, group: "g", score: 50),
            makeFinding(id: 2, group: "g", score: 50),
        ]
        let actions: [Action?] = [.fix, nil]
        let rows = RowBuilder.buildRows(groupBy: .action, findings: findings, actions: actions)
        let headerKeys = rows.compactMap(\.groupKey)
        #expect(headerKeys.contains("fix"))
        #expect(headerKeys.contains("?"))
    }

    @Test func breakdownOmitsZeroBucketsByDefault() {
        let findings = (0..<3).map { makeFinding(id: $0, group: "g", score: 50) }
        let actions: [Action?] = [.fix, .fix, .discuss]
        let breakdown = Tally.breakdown(indices: [0, 1, 2], actions: actions)
        #expect(breakdown == "2 fix · 1 discuss")
    }

    @Test func breakdownKeepsZerosWhenAsked() {
        let actions: [Action?] = [.fix, .fix]
        let breakdown = Tally.breakdown(indices: [0, 1], actions: actions, showZeros: true)
        #expect(breakdown == "2 fix · 0 discuss · 0 skip · 0 ?")
    }

    private func makeFinding(id: Int, group: String, score: Int) -> Finding {
        Finding(
            id: id, title: "T\(id)", group: group, agentLabel: "#\(id): \(group)",
            score: score, explanation: "", file: "f.ts", lineStart: 1, lineEnd: 1,
            language: "typescript", codeExcerpt: "x",
            proposedFix: ProposedFix(explanation: "", edits: [Edit(find: "x", replace: "y")]),
            selection: nil
        )
    }
}
