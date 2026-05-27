import Foundation
import Testing
@testable import ReviewTriage

@Suite("Contract")
struct ContractTests {
    @Test func validInputParses() throws {
        let json = """
        {
          "schemaVersion": 1,
          "title": "Full review · SMA-21: User profile page",
          "branch": "feat/x",
          "mergeBase": "abc123",
          "findings": [{
            "id": 1, "title": "T", "group": "g", "agentLabel": "#1: g", "score": 50,
            "explanation": "e", "file": "f.ts", "lineStart": 1, "lineEnd": 2,
            "language": "typescript", "codeExcerpt": "alpha\\nbeta",
            "proposedFix": {"explanation": "x", "edits": [{"find": "alpha", "replace": "gamma"}]},
            "selection": "fix"
          }]
        }
        """.data(using: .utf8)!
        let input = try Input.parse(json)
        #expect(input.schemaVersion == 1)
        #expect(input.title == "Full review · SMA-21: User profile page")
        #expect(input.findings.count == 1)
        #expect(input.findings[0].selection == .fix)
        #expect(input.findings[0].proposedFix.edits.count == 1)
        #expect(input.findings[0].proposedFix.edits[0].find == "alpha")
    }

    @Test func titleIsOptionalAndDefaultsToEmpty() throws {
        // Earlier rodage builds (0.2.x) emitted no `title`. The parser must
        // tolerate that and produce an empty string so the window falls back
        // to its generic banner — anything stricter would break the upgrade.
        let json = """
        {
          "schemaVersion": 1, "branch": "feat/x", "mergeBase": "abc123",
          "findings": [{
            "id": 1, "title": "T", "group": "g", "agentLabel": "#1: g", "score": 50,
            "explanation": "", "file": "f", "lineStart": 1, "lineEnd": 1,
            "language": "go", "codeExcerpt": "a",
            "proposedFix": {"explanation": "", "edits": [{"find": "a", "replace": "b"}]},
            "selection": null
          }]
        }
        """.data(using: .utf8)!
        let input = try Input.parse(json)
        #expect(input.title == "")
    }

    @Test func schemaMismatchRejected() {
        let json = #"{"schemaVersion": 2, "branch": "", "mergeBase": "", "findings": []}"#
            .data(using: .utf8)!
        #expect(throws: ContractError.unsupportedSchemaVersion(2)) {
            try Input.parse(json)
        }
    }

    @Test func emptyEditsListRejected() {
        let json = """
        {
          "schemaVersion": 1, "branch": "", "mergeBase": "",
          "findings": [{
            "id": 7, "title": "T", "group": "g", "agentLabel": "#7", "score": 0,
            "explanation": "", "file": "f", "lineStart": 1, "lineEnd": 1,
            "language": "go", "codeExcerpt": "x", "proposedFix": {"explanation": "", "edits": []},
            "selection": null
          }]
        }
        """.data(using: .utf8)!
        #expect(throws: ContractError.editsListEmpty(findingId: 7)) {
            try Input.parse(json)
        }
    }

    @Test func emptyFindRejected() {
        let json = """
        {
          "schemaVersion": 1, "branch": "", "mergeBase": "",
          "findings": [{
            "id": 3, "title": "T", "group": "g", "agentLabel": "#3", "score": 0,
            "explanation": "", "file": "f", "lineStart": 1, "lineEnd": 1,
            "language": "go", "codeExcerpt": "x",
            "proposedFix": {"explanation": "", "edits": [{"find": "", "replace": "y"}]},
            "selection": null
          }]
        }
        """.data(using: .utf8)!
        #expect(throws: ContractError.editError(findingId: 3, underlying: .findEmpty(editIndex: 0))) {
            try Input.parse(json)
        }
    }

    @Test func anchorNotFoundRejected() {
        let json = """
        {
          "schemaVersion": 1, "branch": "", "mergeBase": "",
          "findings": [{
            "id": 4, "title": "T", "group": "g", "agentLabel": "#4", "score": 0,
            "explanation": "", "file": "f", "lineStart": 1, "lineEnd": 1,
            "language": "go", "codeExcerpt": "alpha",
            "proposedFix": {"explanation": "", "edits": [{"find": "missing", "replace": "z"}]},
            "selection": null
          }]
        }
        """.data(using: .utf8)!
        #expect(throws: ContractError.editError(findingId: 4, underlying: .notFound(editIndex: 0))) {
            try Input.parse(json)
        }
    }

    @Test func ambiguousAnchorRejected() {
        let json = """
        {
          "schemaVersion": 1, "branch": "", "mergeBase": "",
          "findings": [{
            "id": 5, "title": "T", "group": "g", "agentLabel": "#5", "score": 0,
            "explanation": "", "file": "f", "lineStart": 1, "lineEnd": 1,
            "language": "go", "codeExcerpt": "foo bar foo",
            "proposedFix": {"explanation": "", "edits": [{"find": "foo", "replace": "baz"}]},
            "selection": null
          }]
        }
        """.data(using: .utf8)!
        #expect(throws: ContractError.editError(findingId: 5, underlying: .ambiguous(editIndex: 0, occurrences: 2))) {
            try Input.parse(json)
        }
    }

    @Test func nullSelectionDecodes() throws {
        let json = """
        {
          "schemaVersion": 1, "branch": "", "mergeBase": "",
          "findings": [{
            "id": 1, "title": "T", "group": "g", "agentLabel": "x", "score": 1,
            "explanation": "", "file": "f", "lineStart": 1, "lineEnd": 1,
            "language": "go", "codeExcerpt": "a",
            "proposedFix": {"explanation": "", "edits": [{"find": "a", "replace": "b"}]},
            "selection": null
          }]
        }
        """.data(using: .utf8)!
        let input = try Input.parse(json)
        #expect(input.findings[0].selection == nil)
    }

    @Test func discussDecisionEncodesEmptyPrompt() throws {
        let decision = Decision(id: 1, action: .discuss, discussPrompt: "")
        let data = try JSONEncoder().encode(decision)
        let json = String(data: data, encoding: .utf8)!
        // Empty-string discussPrompt MUST be encoded — the contract distinguishes
        // "discuss with no prompt" (key present, value "") from "non-discuss"
        // (key absent). A field-presence check on the consumer side relies on it.
        #expect(json.contains("\"discussPrompt\""))
        #expect(json.contains("\"\""))
    }

    @Test func fixDecisionOmitsDiscussPrompt() throws {
        let decision = Decision(id: 1, action: .fix)
        let data = try JSONEncoder().encode(decision)
        let json = String(data: data, encoding: .utf8)!
        #expect(!json.contains("discussPrompt"))
    }

    @Test func outputRoundTripsAtomically() throws {
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("rt-out-\(UUID().uuidString).json")
        defer { try? FileManager.default.removeItem(at: url) }

        let output = Output(decisions: [
            Decision(id: 1, action: .fix),
            Decision(id: 2, action: .discuss, discussPrompt: "why?"),
            Decision(id: 3, action: .skip),
        ])
        try output.write(to: url)

        let reloaded = try JSONDecoder().decode(Output.self, from: Data(contentsOf: url))
        #expect(reloaded.schemaVersion == 1)
        #expect(reloaded.decisions.count == 3)
        #expect(reloaded.decisions[1].action == .discuss)
        #expect(reloaded.decisions[1].discussPrompt == "why?")
        #expect(reloaded.decisions[0].discussPrompt == nil)
        let tmp = url.appendingPathExtension("tmp")
        #expect(!FileManager.default.fileExists(atPath: tmp.path))
    }

    @Test func missingFileThrowsCannotRead() {
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("definitely-not-here-\(UUID().uuidString).json")
        #expect(throws: ContractError.self) {
            try Input.load(from: url)
        }
    }
}
