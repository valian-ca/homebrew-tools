import ArgumentParser
import Darwin
import Foundation

@main
enum ReviewTriageCLIEntry {
    static func main() {
        let arguments = Array(CommandLine.arguments.dropFirst())

        if arguments.contains("--help") || arguments.contains("-h") {
            print(ReviewTriageCLI.helpMessage())
            Foundation.exit(0)
        }
        if arguments.contains("--version") || arguments.contains("-v") {
            print(bundleVersion())
            Foundation.exit(0)
        }

        do {
            var command = try ReviewTriageCLI.parseAsRoot(arguments)
            try command.run()
        } catch let exitCode as ExitCode where exitCode == .success {
            Foundation.exit(0)
        } catch let cleanExit as CleanExit {
            let message = ReviewTriageCLI.fullMessage(for: cleanExit)
            if !message.isEmpty { print(message) }
            Foundation.exit(0)
        } catch {
            let message = ReviewTriageCLI.fullMessage(for: error)
            if !message.isEmpty {
                FileHandle.standardError.write(Data("\(message)\n".utf8))
            }
            Foundation.exit(2)
        }
    }
}

struct ReviewTriageCLI: ParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "review-triage",
        abstract: "Triage code-review findings via a native macOS app.",
        discussion: """
            Reads findings from <input> (JSON, schemaVersion: 1), opens the Review
            Triage GUI, and writes decisions to <output> on submit. Designed to be
            invoked by the valian:review Claude Code skill.
            """,
        version: bundleVersion()
    )

    @Option(name: .long, help: "Path to the input JSON file (schemaVersion: 1).")
    var input: String

    @Option(name: .long, help: "Path to write the output JSON file on submit.")
    var output: String

    func run() throws {
        let guiURL = try locateGUIBinary()
        let process = Process()
        process.executableURL = guiURL
        process.arguments = ["--input", input, "--output", output]
        do {
            try process.run()
        } catch {
            FileHandle.standardError.write(Data(
                "error: failed to launch \(guiURL.path): \(error.localizedDescription)\n".utf8
            ))
            throw ExitCode(2)
        }
        process.waitUntilExit()
        if process.terminationReason == .uncaughtSignal {
            FileHandle.standardError.write(Data(
                "error: GUI terminated by signal \(process.terminationStatus)\n".utf8
            ))
            throw ExitCode(2)
        }
        let status = process.terminationStatus
        throw ExitCode((status == 0 || status == 1 || status == 2) ? status : 2)
    }

    private func locateGUIBinary() throws -> URL {
        let cliURL = executablePath().resolvingSymlinksInPath()
        let macOSDir = cliURL.deletingLastPathComponent()
        let guiURL = macOSDir.appendingPathComponent("ReviewTriage")
        guard FileManager.default.fileExists(atPath: guiURL.path) else {
            FileHandle.standardError.write(Data(
                "error: GUI binary not found at \(guiURL.path) — is the .app bundle intact?\n".utf8
            ))
            throw ExitCode(2)
        }
        return guiURL
    }
}

func bundleVersion() -> String {
    let cliURL = executablePath().resolvingSymlinksInPath()
    let infoURL = cliURL
        .deletingLastPathComponent()  // Contents/MacOS
        .deletingLastPathComponent()  // Contents
        .appendingPathComponent("Info.plist")
    if let data = try? Data(contentsOf: infoURL),
       let plist = try? PropertyListSerialization.propertyList(from: data, format: nil) as? [String: Any],
       let version = plist["CFBundleShortVersionString"] as? String {
        return version
    }
    return "dev"
}

/// True on-disk path of the running executable. Unlike `CommandLine.arguments[0]`
/// which is caller-controllable, `_NSGetExecutablePath` reads the kernel record.
func executablePath() -> URL {
    var capacity = UInt32(PATH_MAX)
    var buffer = [CChar](repeating: 0, count: Int(capacity))
    if _NSGetExecutablePath(&buffer, &capacity) != 0 {
        buffer = [CChar](repeating: 0, count: Int(capacity))
        _ = _NSGetExecutablePath(&buffer, &capacity)
    }
    return URL(fileURLWithPath: String(cString: buffer))
}
