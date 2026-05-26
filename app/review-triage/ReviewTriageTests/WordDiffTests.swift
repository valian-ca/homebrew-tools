import Foundation
import Testing
@testable import ReviewTriage

@Suite("WordDiff")
struct WordDiffTests {
    @Test func identicalLinesProduceNoRanges() {
        let (del, add) = WordDiff.emphasisRanges(del: "same", add: "same")
        #expect(del.isEmpty)
        #expect(add.isEmpty)
    }

    @Test func disjointLinesEmphasizeEverything() {
        let (del, add) = WordDiff.emphasisRanges(del: "foo", add: "bar")
        #expect(del.count == 1)
        #expect(add.count == 1)
    }

    @Test func substringChangeIsolatesTheDelta() {
        let delString = "const x = foo();"
        let addString = "const x = bar();"
        let (delRanges, addRanges) = WordDiff.emphasisRanges(del: delString, add: addString)
        #expect(delRanges.count >= 1)
        #expect(addRanges.count >= 1)
        let firstDelRange = delRanges[0]
        let prefixEnd = delString.index(delString.startIndex, offsetBy: "const x = ".count)
        #expect(firstDelRange.lowerBound >= prefixEnd)
    }

    @Test func graphemeClustersCountAsOneCharacterForRanges() {
        let (del, add) = WordDiff.emphasisRanges(del: "café", add: "cafe")
        #expect(!del.isEmpty)
        #expect(!add.isEmpty)
    }
}
