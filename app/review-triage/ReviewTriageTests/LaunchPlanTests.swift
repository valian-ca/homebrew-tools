import Foundation
import Testing
@testable import ReviewTriage

@Suite("LaunchPlan")
struct LaunchPlanTests {
    @Test func missingInputProducesInvalidArgs() {
        let plan = LaunchPlan.parse(args: ["review-triage", "--output", "/tmp/x.json"])
        guard case .invalidArgs(let message) = plan else {
            Issue.record("expected .invalidArgs, got \(plan)")
            return
        }
        #expect(message.contains("--input"))
    }

    @Test func missingOutputProducesInvalidArgs() {
        let plan = LaunchPlan.parse(args: ["review-triage", "--input", "/tmp/x.json"])
        guard case .invalidArgs(let message) = plan else {
            Issue.record("expected .invalidArgs, got \(plan)")
            return
        }
        #expect(message.contains("--output"))
    }

    @Test func missingInputArgValueProducesInvalidArgs() {
        let plan = LaunchPlan.parse(args: ["review-triage", "--input"])
        guard case .invalidArgs = plan else {
            Issue.record("expected .invalidArgs, got \(plan)")
            return
        }
    }

    @Test func malformedJsonProducesSchemaError() throws {
        let inputURL = try writeTempFile(content: "{ not json")
        let outputURL = FileManager.default.temporaryDirectory
            .appendingPathComponent("rt-out-\(UUID().uuidString).json")
        defer { try? FileManager.default.removeItem(at: inputURL) }
        let plan = LaunchPlan.parse(args: [
            "review-triage", "--input", inputURL.path, "--output", outputURL.path,
        ])
        guard case .schemaError = plan else {
            Issue.record("expected .schemaError, got \(plan)")
            return
        }
    }

    @Test func mismatchedSchemaVersionProducesSchemaError() throws {
        let inputURL = try writeTempFile(content: #"{"schemaVersion": 99, "branch": "", "mergeBase": "", "findings": []}"#)
        let outputURL = FileManager.default.temporaryDirectory
            .appendingPathComponent("rt-out-\(UUID().uuidString).json")
        defer { try? FileManager.default.removeItem(at: inputURL) }
        let plan = LaunchPlan.parse(args: [
            "review-triage", "--input", inputURL.path, "--output", outputURL.path,
        ])
        guard case .schemaError(let error) = plan else {
            Issue.record("expected .schemaError, got \(plan)")
            return
        }
        guard case .unsupportedSchemaVersion(let v) = error else {
            Issue.record("expected unsupportedSchemaVersion, got \(error)")
            return
        }
        #expect(v == 99)
    }

    @Test func emptyFindingsTriggersEmptyOutputPath() throws {
        let inputURL = try writeTempFile(content: #"{"schemaVersion": 1, "branch": "", "mergeBase": "", "findings": []}"#)
        let outputURL = FileManager.default.temporaryDirectory
            .appendingPathComponent("rt-out-\(UUID().uuidString).json")
        defer { try? FileManager.default.removeItem(at: inputURL) }
        let plan = LaunchPlan.parse(args: [
            "review-triage", "--input", inputURL.path, "--output", outputURL.path,
        ])
        guard case .emptyFindings(let returnedURL) = plan else {
            Issue.record("expected .emptyFindings, got \(plan)")
            return
        }
        #expect(returnedURL.path == outputURL.path)
    }

    @Test func validInputProducesReady() throws {
        let json = """
        {
          "schemaVersion": 1, "branch": "b", "mergeBase": "m",
          "findings": [{
            "id": 1, "title": "T", "group": "g", "agentLabel": "#1: g", "score": 50,
            "explanation": "", "file": "f", "lineStart": 1, "lineEnd": 1,
            "language": "go", "codeExcerpt": "a", "proposedFix": {"explanation": "", "edits": [{"find": "a", "replace": "b"}]},
            "selection": null
          }]
        }
        """
        let inputURL = try writeTempFile(content: json)
        let outputURL = FileManager.default.temporaryDirectory
            .appendingPathComponent("rt-out-\(UUID().uuidString).json")
        defer { try? FileManager.default.removeItem(at: inputURL) }
        let plan = LaunchPlan.parse(args: [
            "review-triage", "--input", inputURL.path, "--output", outputURL.path,
        ])
        guard case .ready(let input, let returnedURL) = plan else {
            Issue.record("expected .ready, got \(plan)")
            return
        }
        #expect(input.findings.count == 1)
        #expect(returnedURL.path == outputURL.path)
    }

    @Test func injectedMacOSLaunchFlagsAreTolerated() throws {
        let inputURL = try writeTempFile(content: #"{"schemaVersion": 1, "branch": "", "mergeBase": "", "findings": []}"#)
        let outputURL = FileManager.default.temporaryDirectory
            .appendingPathComponent("rt-out-\(UUID().uuidString).json")
        defer { try? FileManager.default.removeItem(at: inputURL) }
        let plan = LaunchPlan.parse(args: [
            "review-triage",
            "-NSDocumentRevisionsDebugMode", "YES",
            "--input", inputURL.path,
            "--output", outputURL.path,
        ])
        guard case .emptyFindings = plan else {
            Issue.record("expected .emptyFindings, got \(plan)")
            return
        }
    }

    private func writeTempFile(content: String) throws -> URL {
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("rt-input-\(UUID().uuidString).json")
        try content.write(to: url, atomically: true, encoding: .utf8)
        return url
    }
}
