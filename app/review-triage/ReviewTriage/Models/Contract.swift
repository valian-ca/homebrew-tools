import Foundation

public enum Contract {
    public static let schemaVersion = 1
}

public enum Action: String, Codable, Sendable, CaseIterable, Hashable {
    case fix
    case skip
    case discuss
}

public struct Edit: Codable, Sendable, Hashable {
    public let find: String
    public let replace: String

    public init(find: String, replace: String) {
        self.find = find
        self.replace = replace
    }
}

public struct ProposedFix: Codable, Sendable, Hashable {
    public let explanation: String
    public let edits: [Edit]

    public init(explanation: String, edits: [Edit]) {
        self.explanation = explanation
        self.edits = edits
    }
}

public struct Finding: Codable, Sendable, Hashable, Identifiable {
    public let id: Int
    public let title: String
    public let group: String
    public let agentLabel: String
    public let score: Int
    public let explanation: String
    public let file: String
    public let lineStart: Int
    public let lineEnd: Int
    public let language: String
    public let codeExcerpt: String
    public let proposedFix: ProposedFix
    public let selection: Action?

    public init(
        id: Int, title: String, group: String, agentLabel: String, score: Int,
        explanation: String, file: String, lineStart: Int, lineEnd: Int,
        language: String, codeExcerpt: String, proposedFix: ProposedFix,
        selection: Action?
    ) {
        self.id = id
        self.title = title
        self.group = group
        self.agentLabel = agentLabel
        self.score = score
        self.explanation = explanation
        self.file = file
        self.lineStart = lineStart
        self.lineEnd = lineEnd
        self.language = language
        self.codeExcerpt = codeExcerpt
        self.proposedFix = proposedFix
        self.selection = selection
    }
}

public struct Input: Codable, Sendable {
    public let schemaVersion: Int
    public let title: String
    public let branch: String
    public let mergeBase: String
    public let findings: [Finding]

    public init(schemaVersion: Int, title: String = "", branch: String, mergeBase: String, findings: [Finding]) {
        self.schemaVersion = schemaVersion
        self.title = title
        self.branch = branch
        self.mergeBase = mergeBase
        self.findings = findings
    }

    /// Tolerate inputs from the 0.2.x rodage that omit `title` — decode it as
    /// an empty string rather than failing the whole parse. The window then
    /// falls back to a generic banner; everything else still works.
    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        schemaVersion = try container.decode(Int.self, forKey: .schemaVersion)
        title = try container.decodeIfPresent(String.self, forKey: .title) ?? ""
        branch = try container.decode(String.self, forKey: .branch)
        mergeBase = try container.decode(String.self, forKey: .mergeBase)
        findings = try container.decode([Finding].self, forKey: .findings)
    }

    enum CodingKeys: String, CodingKey {
        case schemaVersion, title, branch, mergeBase, findings
    }

    public static func parse(_ data: Data) throws -> Input {
        let input: Input
        do {
            input = try JSONDecoder().decode(Input.self, from: data)
        } catch {
            throw ContractError.malformedInput(underlying: error)
        }
        try input.validate()
        return input
    }

    public static func load(from url: URL) throws -> Input {
        let data: Data
        do {
            data = try Data(contentsOf: url)
        } catch {
            throw ContractError.cannotReadInput(path: url.path, underlying: error)
        }
        return try parse(data)
    }

    public func validate() throws {
        guard schemaVersion == Contract.schemaVersion else {
            throw ContractError.unsupportedSchemaVersion(schemaVersion)
        }
        for finding in findings {
            if finding.proposedFix.edits.isEmpty {
                throw ContractError.editsListEmpty(findingId: finding.id)
            }
            // Materialize the edits up-front so the developer never reaches the
            // diff pane with a finding whose anchors don't apply. EditApply is
            // pure; we throw away the result and recompute it at render time.
            do {
                _ = try EditApply.apply(edits: finding.proposedFix.edits, to: finding.codeExcerpt)
            } catch let err as EditApplyError {
                throw ContractError.editError(findingId: finding.id, underlying: err)
            }
        }
    }
}

public struct Decision: Codable, Sendable, Hashable {
    public let id: Int
    public let action: Action
    public let discussPrompt: String?

    public init(id: Int, action: Action, discussPrompt: String? = nil) {
        self.id = id
        self.action = action
        self.discussPrompt = discussPrompt
    }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        id = try container.decode(Int.self, forKey: .id)
        action = try container.decode(Action.self, forKey: .action)
        let rawPrompt = try container.decodeIfPresent(String.self, forKey: .discussPrompt)
        discussPrompt = (action == .discuss) ? rawPrompt : nil
    }

    public func encode(to encoder: Encoder) throws {
        var container = encoder.container(keyedBy: CodingKeys.self)
        try container.encode(id, forKey: .id)
        try container.encode(action, forKey: .action)
        if action == .discuss {
            try container.encode(discussPrompt ?? "", forKey: .discussPrompt)
        }
    }

    enum CodingKeys: String, CodingKey {
        case id, action, discussPrompt
    }
}

public struct Output: Codable, Sendable {
    public let schemaVersion: Int
    public let decisions: [Decision]

    public init(decisions: [Decision]) {
        self.schemaVersion = Contract.schemaVersion
        self.decisions = decisions
    }

    public func write(to url: URL) throws {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
        var data = try encoder.encode(self)
        data.append(0x0A) // trailing newline, mirrors the Go atomic Write
        let tempURL = url.appendingPathExtension("tmp")
        try data.write(to: tempURL, options: [.atomic])
        if FileManager.default.fileExists(atPath: url.path) {
            _ = try FileManager.default.replaceItemAt(url, withItemAt: tempURL)
        } else {
            try FileManager.default.moveItem(at: tempURL, to: url)
        }
    }
}

public enum ContractError: Error, LocalizedError, Equatable {
    case unsupportedSchemaVersion(Int)
    case malformedInput(underlying: Error)
    case editsListEmpty(findingId: Int)
    case editError(findingId: Int, underlying: EditApplyError)
    case cannotReadInput(path: String, underlying: Error)

    public var errorDescription: String? {
        switch self {
        case .unsupportedSchemaVersion(let v):
            return "unsupported schemaVersion \(v) (expected \(Contract.schemaVersion))"
        case .malformedInput(let underlying):
            return "malformed input JSON: \(underlying.localizedDescription)"
        case .editsListEmpty(let id):
            return "finding \(id) has an empty proposedFix.edits list"
        case .editError(let id, let underlying):
            return "finding \(id): \(underlying.errorDescription ?? "edit application failed")"
        case .cannotReadInput(let path, let underlying):
            return "cannot read input file \(path): \(underlying.localizedDescription)"
        }
    }

    public static func == (lhs: ContractError, rhs: ContractError) -> Bool {
        switch (lhs, rhs) {
        case (.unsupportedSchemaVersion(let a), .unsupportedSchemaVersion(let b)):
            return a == b
        case (.editsListEmpty(let a), .editsListEmpty(let b)):
            return a == b
        case (.editError(let a, let ea), .editError(let b, let eb)):
            return a == b && ea == eb
        case (.malformedInput, .malformedInput),
             (.cannotReadInput, .cannotReadInput):
            return true
        default:
            return false
        }
    }
}
