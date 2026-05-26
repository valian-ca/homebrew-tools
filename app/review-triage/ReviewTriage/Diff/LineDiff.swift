import Foundation

public struct DiffLine: Sendable, Equatable {
    public enum Kind: Sendable, Equatable { case context, add, delete }

    public let kind: Kind
    public let oldNum: Int
    public let newNum: Int
    public let text: String
    public let emphasisRanges: [Range<String.Index>]
}

public enum LineDiff {
    public static func diff(oldCode: String, newCode: String, startLine: Int) -> [DiffLine] {
        let oldLines = splitLines(oldCode)
        let newLines = splitLines(newCode)
        let ops = MyersDiff.diff(oldLines, newLines)

        var out: [DiffLine] = []
        var oldNum = startLine
        var newNum = startLine
        var i = 0
        while i < ops.count {
            switch ops[i] {
            case .equal(let text):
                out.append(DiffLine(kind: .context, oldNum: oldNum, newNum: newNum, text: text, emphasisRanges: []))
                oldNum += 1
                newNum += 1
                i += 1
            case .delete:
                let delStart = i
                while i < ops.count, case .delete = ops[i] { i += 1 }
                let dels = ops[delStart..<i].compactMap { op -> String? in
                    if case .delete(let t) = op { return t } else { return nil }
                }
                let addStart = i
                while i < ops.count, case .insert = ops[i] { i += 1 }
                let adds = ops[addStart..<i].compactMap { op -> String? in
                    if case .insert(let t) = op { return t } else { return nil }
                }
                emitReplace(out: &out, dels: dels, adds: adds, oldNum: &oldNum, newNum: &newNum)
            case .insert(let text):
                out.append(DiffLine(kind: .add, oldNum: 0, newNum: newNum, text: text, emphasisRanges: []))
                newNum += 1
                i += 1
            }
        }
        return out
    }

    private static func splitLines(_ s: String) -> [String] {
        if s.isEmpty { return [] }
        var lines = s.components(separatedBy: "\n")
        if lines.last == "" {
            // Drop the empty tail left by a trailing newline so line numbering
            // doesn't include a phantom blank line.
            lines.removeLast()
        }
        return lines
    }

    private static func emitReplace(
        out: inout [DiffLine],
        dels: [String], adds: [String],
        oldNum: inout Int, newNum: inout Int
    ) {
        // Equal-length runs: pair each delete with its corresponding add and
        // compute word-level emphasis for the pair. The Go TUI does the same:
        // it's the case where word-by-word coloring is most informative.
        if dels.count == adds.count, !dels.isEmpty {
            var delEmph: [[Range<String.Index>]] = []
            var addEmph: [[Range<String.Index>]] = []
            for (del, add) in zip(dels, adds) {
                let (de, ae) = WordDiff.emphasisRanges(del: del, add: add)
                delEmph.append(de)
                addEmph.append(ae)
            }
            // Mirror Go's ordering: all deletes first, then all adds — keeps
            // the visual grouping consistent with how the TUI renders.
            for (i, del) in dels.enumerated() {
                out.append(DiffLine(kind: .delete, oldNum: oldNum, newNum: 0, text: del, emphasisRanges: delEmph[i]))
                oldNum += 1
            }
            for (i, add) in adds.enumerated() {
                out.append(DiffLine(kind: .add, oldNum: 0, newNum: newNum, text: add, emphasisRanges: addEmph[i]))
                newNum += 1
            }
            return
        }
        for del in dels {
            out.append(DiffLine(kind: .delete, oldNum: oldNum, newNum: 0, text: del, emphasisRanges: []))
            oldNum += 1
        }
        for add in adds {
            out.append(DiffLine(kind: .add, oldNum: 0, newNum: newNum, text: add, emphasisRanges: []))
            newNum += 1
        }
    }
}
