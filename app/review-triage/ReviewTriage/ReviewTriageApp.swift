import SwiftUI
import AppKit

@main
struct ReviewTriageApp: App {
    @State private var launchOutcome: LaunchOutcome
    @NSApplicationDelegateAdaptor(QuitGuard.self) private var quitGuard
    @AppStorage(AppTheme.defaultsKey) private var appTheme: AppTheme = .auto
    @AppStorage(AppSettingsKeys.textFontSize) private var textFontSize: Double = AppSettingsDefaults.textFontSize
    @AppStorage(AppSettingsKeys.codeFontSize) private var codeFontSize: Double = AppSettingsDefaults.codeFontSize
    @AppStorage(AppSettingsKeys.textFontFamily) private var textFontFamily: String = AppSettingsDefaults.textFontFamily
    @AppStorage(AppSettingsKeys.codeFontFamily) private var codeFontFamily: String = AppSettingsDefaults.codeFontFamily

    init() {
        // Single-instance synchronous tool — the CLI shim blocks until this
        // process exits, so window tabs don't fit the model (a second tab
        // would have no input and confuse the developer). Turn the feature
        // off globally so `⌘T` and the View → Show Tab Bar item disappear.
        NSWindow.allowsAutomaticWindowTabbing = false

        let args = CommandLine.arguments

        if Self.isRunningHostedTests() {
            _launchOutcome = State(initialValue: .testHost)
            return
        }
        if args.contains("--help") || args.contains("-h") {
            print(Self.helpText)
            Foundation.exit(0)
        }
        if args.contains("--version") || args.contains("-v") {
            print(Self.bundleVersion())
            Foundation.exit(0)
        }

        switch LaunchPlan.parse(args: args) {
        case .invalidArgs(let message):
            Self.writeStderr("error: \(message)")
            Foundation.exit(2)
        case .schemaError(let error):
            Self.writeStderr("error: \(error.localizedDescription)")
            Foundation.exit(2)
        case .emptyFindings(let url):
            do {
                try Output(decisions: []).write(to: url)
                Foundation.exit(0)
            } catch {
                Self.writeStderr("error: cannot write empty output to \(url.path): \(error.localizedDescription)")
                Foundation.exit(2)
            }
        case .ready(let input, let url):
            _launchOutcome = State(initialValue: .ready(TriageState(input: input, outputURL: url)))
        }
    }

    var body: some Scene {
        WindowGroup(id: "triage") {
            content
                .frame(minWidth: 900, minHeight: 600)
                .navigationTitle(windowTitle)
                .preferredColorScheme(appTheme.colorScheme)
                .environment(\.fontPalette, palette)
        }
        .windowResizability(.contentSize)
        .commands {
            CommandsMenu(state: launchOutcome.triageState)
        }

        Settings {
            SettingsView()
                .preferredColorScheme(appTheme.colorScheme)
                .environment(\.fontPalette, palette)
        }
    }

    private var palette: FontPalette {
        FontPalette(
            textSize: textFontSize,
            codeSize: codeFontSize,
            textFamily: textFontFamily,
            codeFamily: codeFontFamily
        )
    }

    private var windowTitle: String {
        switch launchOutcome {
        case .ready(let state) where !state.inputTitle.isEmpty:
            return state.inputTitle
        case .ready, .testHost:
            return "Review Triage"
        }
    }

    @ViewBuilder
    private var content: some View {
        switch launchOutcome {
        case .testHost:
            // Hosted test bundle: a 1pt invisible view keeps the App alive
            // without showing anything to the user. xctest takes over from here.
            Color.clear.frame(width: 1, height: 1)
        case .ready(let state):
            TriageWindow(state: state)
        }
    }

    private static func isRunningHostedTests() -> Bool {
        let env = ProcessInfo.processInfo.environment
        return env["XCTestConfigurationFilePath"] != nil
            || env["XCTestBundlePath"] != nil
            || env["XCTestSessionIdentifier"] != nil
    }

    static func writeStderr(_ message: String) {
        FileHandle.standardError.write(Data("\(message)\n".utf8))
    }

    static func bundleVersion() -> String {
        Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String ?? "dev"
    }

    static let helpText = """
        review-triage — triage code-review findings via a native macOS app.

        USAGE
          review-triage --input <path> --output <path>

        OPTIONS
          --input <path>    JSON file produced by the valian:review skill
                            (schemaVersion: 1, see VAL-235).
          --output <path>   JSON file to write decisions to on submit.
          --help, -h        Print this help and exit.
          --version, -v     Print the version and exit.

        EXIT CODES
          0  Output written successfully.
          1  Developer cancelled (no output written).
          2  Input malformed, schemaVersion ≠ 1, or other internal error.

        EXAMPLE
          review-triage \\
            --input /tmp/review-findings.json \\
            --output /tmp/review-decisions.json
        """
}

private enum LaunchOutcome {
    case testHost
    case ready(TriageState)

    var triageState: TriageState? {
        if case .ready(let state) = self { return state }
        return nil
    }
}

final class QuitGuard: NSObject, NSApplicationDelegate {
    nonisolated(unsafe) static var submitDidSucceed = false

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
        true
    }

    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        if Self.submitDidSucceed { return .terminateNow }
        // Skip the prompt during hosted tests so xctest can shut the app down
        // without modal interference.
        let env = ProcessInfo.processInfo.environment
        if env["XCTestConfigurationFilePath"] != nil { return .terminateNow }

        let alert = NSAlert()
        alert.messageText = "Quit without submitting?"
        alert.informativeText = "All decisions will be lost."
        alert.alertStyle = .warning
        alert.addButton(withTitle: "Cancel")
        alert.addButton(withTitle: "Quit")
        let response = alert.runModal()
        if response == .alertSecondButtonReturn {
            // User confirmed abandon — exit with code 1 (cancel), no output file.
            Foundation.exit(1)
        }
        return .terminateCancel
    }
}
