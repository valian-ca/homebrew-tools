import Foundation

public enum EditApply {
    /// Apply an ordered list of `find → replace` edits to `source`, returning
    /// the post-edit string. Each edit is validated against the *in-progress*
    /// text (so earlier edits can introduce, remove, or duplicate anchors for
    /// later edits — and we surface the error at the edit that fails, not
    /// retroactively).
    public static func apply(edits: [Edit], to source: String) throws -> String {
        var current = source
        for (index, edit) in edits.enumerated() {
            if edit.find.isEmpty {
                throw EditApplyError.findEmpty(editIndex: index)
            }
            let ranges = current.ranges(of: edit.find)
            switch ranges.count {
            case 0:
                throw EditApplyError.notFound(editIndex: index)
            case 1:
                current.replaceSubrange(ranges[0], with: edit.replace)
            default:
                throw EditApplyError.ambiguous(editIndex: index, occurrences: ranges.count)
            }
        }
        return current
    }
}

public enum EditApplyError: Error, LocalizedError, Equatable {
    case findEmpty(editIndex: Int)
    case notFound(editIndex: Int)
    case ambiguous(editIndex: Int, occurrences: Int)

    public var errorDescription: String? {
        switch self {
        case .findEmpty(let i):
            return "edit \(i) has an empty `find` (an anchor is required)"
        case .notFound(let i):
            return "edit \(i): `find` not present in codeExcerpt"
        case .ambiguous(let i, let n):
            return "edit \(i): `find` matches \(n) times in codeExcerpt (must be unique)"
        }
    }
}
