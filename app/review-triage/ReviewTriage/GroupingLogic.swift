import Foundation

public enum GroupBy: Int, CaseIterable, Sendable {
    case type
    case score
    case action
    case file

    public var label: String {
        switch self {
        case .type: return "type"
        case .score: return "score"
        case .action: return "action"
        case .file: return "file"
        }
    }

    public func next() -> GroupBy {
        let all = GroupBy.allCases
        let i = all.firstIndex(of: self) ?? 0
        return all[(i + 1) % all.count]
    }

    public func key(for finding: Finding, action: Action?) -> String {
        switch self {
        case .type: return finding.group
        case .score: return ScoreBucket.label(for: finding.score)
        case .action: return action?.rawValue ?? "?"
        case .file: return finding.file
        }
    }
}

public enum ScoreBucket {
    // Bands match the Go TUI exactly so grouping is identical across both UIs.
    public static let canonicalOrder: [String] = ["100", "<100", "≤75", "≤50", "≤25", "0"]

    public static func label(for score: Int) -> String {
        switch score {
        case 100: return "100"
        case 0: return "0"
        case 76...99: return "<100"
        case 51...75: return "≤75"
        case 26...50: return "≤50"
        default: return "≤25"
        }
    }
}

public enum RowKind: Sendable, Equatable, Hashable {
    case header(String)   // groupKey lives in the case
    case item(Int)        // findingIdx lives in the case
    case submit
}

public struct Row: Identifiable, Sendable, Equatable, Hashable {
    public let kind: RowKind
    public var id: RowKind { kind }

    public init(kind: RowKind) {
        self.kind = kind
    }

    /// Convenience: returns the group key when this row is a header, else nil.
    public var groupKey: String? {
        if case .header(let key) = kind { return key }
        return nil
    }

    /// Convenience: returns the finding index when this row is an item, else nil.
    public var findingIdx: Int? {
        if case .item(let idx) = kind { return idx }
        return nil
    }
}

public enum RowBuilder {
    public static func buildRows(
        groupBy: GroupBy,
        findings: [Finding],
        actions: [Action?]
    ) -> [Row] {
        precondition(findings.count == actions.count,
                     "findings and actions arrays must be the same length")

        var groups: [(key: String, indices: [Int])] = []
        var groupIndex: [String: Int] = [:]
        for (i, finding) in findings.enumerated() {
            let key = groupBy.key(for: finding, action: actions[i])
            if let existing = groupIndex[key] {
                groups[existing].indices.append(i)
            } else {
                groupIndex[key] = groups.count
                groups.append((key: key, indices: [i]))
            }
        }

        // Score buckets use a fixed visual order; the other dimensions keep
        // the order of first encounter — matches the Go TUI's behaviour.
        if groupBy == .score {
            groups.sort { a, b in
                let ai = ScoreBucket.canonicalOrder.firstIndex(of: a.key) ?? ScoreBucket.canonicalOrder.count
                let bi = ScoreBucket.canonicalOrder.firstIndex(of: b.key) ?? ScoreBucket.canonicalOrder.count
                return ai < bi
            }
        }

        var rows: [Row] = []
        for group in groups {
            rows.append(Row(kind: .header(group.key)))
            // Higher score wins within a group, matching the TUI default.
            let sorted = group.indices.sorted { findings[$0].score > findings[$1].score }
            for idx in sorted {
                rows.append(Row(kind: .item(idx)))
            }
        }
        rows.append(Row(kind: .submit))
        return rows
    }
}

public enum Tally {
    public static func counts(indices: [Int], actions: [Action?]) -> [Action?: Int] {
        var counts: [Action?: Int] = [.fix: 0, .skip: 0, .discuss: 0, nil: 0]
        for idx in indices {
            counts[actions[idx], default: 0] += 1
        }
        return counts
    }

    // `showZeros` keeps every bucket visible for the footer where layout
    // stability matters; group headers pass false to elide empty buckets.
    public static func breakdown(indices: [Int], actions: [Action?], showZeros: Bool = false) -> String {
        let counts = counts(indices: indices, actions: actions)
        let order: [(Action?, String)] = [(.fix, "fix"), (.discuss, "discuss"), (.skip, "skip"), (nil, "?")]
        let parts = order.compactMap { (action, label) -> String? in
            let count = counts[action, default: 0]
            guard count > 0 || showZeros else { return nil }
            return "\(count) \(label)"
        }
        return parts.joined(separator: " · ")
    }
}
