import Foundation

public enum LaunchPlan {
    case invalidArgs(message: String)
    case schemaError(ContractError)
    /// Empty findings short-circuit: write `decisions: []` and exit without
    /// opening a window. Matches the Go TUI.
    case emptyFindings(outputURL: URL)
    case ready(input: Input, outputURL: URL)

    public static func parse(args: [String]) -> LaunchPlan {
        var inputPath: String?
        var outputPath: String?
        var i = 1
        while i < args.count {
            let arg = args[i]
            switch arg {
            case "--input":
                guard i + 1 < args.count else {
                    return .invalidArgs(message: "--input requires a path argument")
                }
                inputPath = args[i + 1]
                i += 2
            case "--output":
                guard i + 1 < args.count else {
                    return .invalidArgs(message: "--output requires a path argument")
                }
                outputPath = args[i + 1]
                i += 2
            default:
                // Unknown args are tolerated — macOS may inject -NSDocumentRevisionsDebugMode
                // or similar in some launch contexts; ignore them rather than fail.
                i += 1
            }
        }

        guard let inputPath else {
            return .invalidArgs(message: "missing required --input <path>")
        }
        guard let outputPath else {
            return .invalidArgs(message: "missing required --output <path>")
        }

        let outputURL = URL(fileURLWithPath: outputPath)
        let inputURL = URL(fileURLWithPath: inputPath)

        let input: Input
        do {
            input = try Input.load(from: inputURL)
        } catch let error as ContractError {
            return .schemaError(error)
        } catch {
            return .schemaError(.malformedInput(underlying: error))
        }

        if input.findings.isEmpty {
            return .emptyFindings(outputURL: outputURL)
        }
        return .ready(input: input, outputURL: outputURL)
    }
}
