import SwiftUI

struct ConfirmSheet: View {
    @Environment(\.fontPalette) private var palette
    @Bindable var state: TriageState

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("Submit triage decisions?")
                .font(palette.heading)
            VStack(alignment: .leading, spacing: 6) {
                Text(state.planSummary)
                    .font(palette.code)
                if state.discussWithoutPromptCount > 0 {
                    Label(
                        "\(state.discussWithoutPromptCount) discuss item\(state.discussWithoutPromptCount == 1 ? "" : "s") without a prompt — you can still ship as-is.",
                        systemImage: "exclamationmark.triangle"
                    )
                    .font(palette.body)
                    .foregroundStyle(.secondary)
                }
            }
            HStack {
                Spacer()
                Button("Back") {
                    state.submitConfirmShowing = false
                }
                .keyboardShortcut(.cancelAction)
                Button("Ship") {
                    submit()
                }
                .keyboardShortcut(.defaultAction)
            }
        }
        .padding(24)
        .frame(minWidth: 420)
    }

    private func submit() {
        let output = state.finalize()
        do {
            try output.write(to: state.outputURL)
            // Tell the QuitGuard the submit succeeded so the terminate hook
            // doesn't show its abandon prompt and overwrite our exit code.
            QuitGuard.submitDidSucceed = true
            Foundation.exit(0)
        } catch {
            state.footerMessage = "Failed to write output: \(error.localizedDescription)"
            state.submitConfirmShowing = false
        }
    }
}
