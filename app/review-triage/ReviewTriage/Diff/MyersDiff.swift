import Foundation

public enum MyersDiff {
    public enum Op<T: Sendable>: Sendable {
        case equal(T)
        case delete(T)
        case insert(T)
    }

    public static func diff<T: Equatable & Sendable>(_ a: [T], _ b: [T]) -> [Op<T>] {
        let n = a.count
        let m = b.count
        if n == 0 { return b.map { .insert($0) } }
        if m == 0 { return a.map { .delete($0) } }

        var dp = Array(repeating: Array(repeating: 0, count: m + 1), count: n + 1)
        for i in 1...n {
            for j in 1...m {
                if a[i - 1] == b[j - 1] {
                    dp[i][j] = dp[i - 1][j - 1] + 1
                } else {
                    dp[i][j] = max(dp[i - 1][j], dp[i][j - 1])
                }
            }
        }

        var ops: [Op<T>] = []
        var i = n
        var j = m
        while i > 0 || j > 0 {
            if i > 0 && j > 0 && a[i - 1] == b[j - 1] {
                ops.append(.equal(a[i - 1]))
                i -= 1
                j -= 1
            } else if j > 0 && (i == 0 || dp[i][j - 1] >= dp[i - 1][j]) {
                ops.append(.insert(b[j - 1]))
                j -= 1
            } else {
                ops.append(.delete(a[i - 1]))
                i -= 1
            }
        }
        return ops.reversed()
    }
}
