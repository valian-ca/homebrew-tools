import Foundation
import Testing
@testable import ReviewTriage

@Suite("LineDiff")
struct LineDiffTests {
    @Test func pureAdditionHasOnlyAdds() {
        let lines = LineDiff.diff(oldCode: "", newCode: "alpha\nbeta\n", startLine: 10)
        let allAdds = lines.allSatisfy { $0.kind == .add }
        let allOldZero = lines.allSatisfy { $0.oldNum == 0 }
        #expect(lines.count == 2)
        #expect(allAdds)
        #expect(lines[0].newNum == 10)
        #expect(lines[1].newNum == 11)
        #expect(allOldZero)
    }

    @Test func pureDeletionHasOnlyDeletes() {
        let lines = LineDiff.diff(oldCode: "alpha\nbeta\n", newCode: "", startLine: 5)
        let allDeletes = lines.allSatisfy { $0.kind == .delete }
        let allNewZero = lines.allSatisfy { $0.newNum == 0 }
        #expect(lines.count == 2)
        #expect(allDeletes)
        #expect(lines[0].oldNum == 5)
        #expect(lines[1].oldNum == 6)
        #expect(allNewZero)
    }

    @Test func contextLinesPreserveNumbering() {
        let old = "a\nb\nc\n"
        let new = "a\nB\nc\n"  // change middle line only
        let lines = LineDiff.diff(oldCode: old, newCode: new, startLine: 100)
        #expect(lines.count == 4)
        #expect(lines[0].kind == .context)
        #expect(lines[0].oldNum == 100 && lines[0].newNum == 100)
        #expect(lines[1].kind == .delete)
        #expect(lines[1].oldNum == 101)
        #expect(lines[2].kind == .add)
        #expect(lines[2].newNum == 101)
        #expect(lines[3].kind == .context)
        #expect(lines[3].oldNum == 102 && lines[3].newNum == 102)
    }

    @Test func equalLengthReplaceCarriesWordEmphasis() {
        let lines = LineDiff.diff(
            oldCode: "const x = foo();\n",
            newCode: "const x = bar();\n",
            startLine: 1
        )
        let delLine = lines.first { $0.kind == .delete }
        let addLine = lines.first { $0.kind == .add }
        #expect(delLine != nil)
        #expect(addLine != nil)
        #expect(!delLine!.emphasisRanges.isEmpty)
        #expect(!addLine!.emphasisRanges.isEmpty)
    }

    @Test func unequalLengthReplaceSkipsWordEmphasis() {
        let lines = LineDiff.diff(
            oldCode: "alpha\nbeta\n",
            newCode: "gamma\n",
            startLine: 1
        )
        let dels = lines.filter { $0.kind == .delete }
        let adds = lines.filter { $0.kind == .add }
        let delsNoEmph = dels.allSatisfy { $0.emphasisRanges.isEmpty }
        let addsNoEmph = adds.allSatisfy { $0.emphasisRanges.isEmpty }
        #expect(dels.count == 2)
        #expect(adds.count == 1)
        #expect(delsNoEmph)
        #expect(addsNoEmph)
    }

    @Test func trailingNewlineDoesNotProducePhantomLine() {
        let withNL = LineDiff.diff(oldCode: "x\n", newCode: "x\n", startLine: 1)
        let withoutNL = LineDiff.diff(oldCode: "x", newCode: "x", startLine: 1)
        #expect(withNL.count == 1)
        #expect(withoutNL.count == 1)
    }
}
