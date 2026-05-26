import Foundation

public enum WordDiff {
    public static func emphasisRanges(
        del: String, add: String
    ) -> (delRanges: [Range<String.Index>], addRanges: [Range<String.Index>]) {
        // Cap the character-level diff to keep the DP table bounded.
        // Beyond this length the visual benefit of word-level emphasis is
        // marginal (huge lines are minified or generated) and the cost
        // (O(n·m) Int table) becomes a denial-of-service vector.
        let maxChars = 2000
        if del.count > maxChars || add.count > maxChars {
            return ([], [])
        }
        let delChars = Array(del)
        let addChars = Array(add)
        let ops = MyersDiff.diff(delChars, addChars)

        var delRanges: [Range<String.Index>] = []
        var addRanges: [Range<String.Index>] = []
        var delIdx = del.startIndex
        var addIdx = add.startIndex
        var pendingDeleteStart: String.Index?
        var pendingInsertStart: String.Index?

        func flushDelete() {
            if let start = pendingDeleteStart {
                delRanges.append(start..<delIdx)
                pendingDeleteStart = nil
            }
        }
        func flushInsert() {
            if let start = pendingInsertStart {
                addRanges.append(start..<addIdx)
                pendingInsertStart = nil
            }
        }

        for op in ops {
            switch op {
            case .equal:
                flushDelete()
                flushInsert()
                if delIdx < del.endIndex { delIdx = del.index(after: delIdx) }
                if addIdx < add.endIndex { addIdx = add.index(after: addIdx) }
            case .delete:
                if pendingDeleteStart == nil { pendingDeleteStart = delIdx }
                if delIdx < del.endIndex { delIdx = del.index(after: delIdx) }
            case .insert:
                if pendingInsertStart == nil { pendingInsertStart = addIdx }
                if addIdx < add.endIndex { addIdx = add.index(after: addIdx) }
            }
        }
        flushDelete()
        flushInsert()

        return (delRanges, addRanges)
    }
}
