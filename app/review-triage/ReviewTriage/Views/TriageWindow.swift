import SwiftUI

struct TriageWindow: View {
    @Bindable var state: TriageState

    var body: some View {
        NavigationSplitView {
            Sidebar(state: state)
                .navigationSplitViewColumnWidth(min: 300, ideal: 380, max: 520)
        } detail: {
            DetailPane(state: state)
                .frame(minWidth: 520)
        }
        .navigationSplitViewStyle(.balanced)
        .sheet(isPresented: $state.discussShowing) {
            DiscussSheet(state: state)
        }
        .onChange(of: state.discussShowing) { _, isShowing in
            if !isShowing && state.discussingFindingIdx != nil {
                state.cancelDiscuss()
            }
        }
        .sheet(isPresented: $state.submitConfirmShowing) {
            ConfirmSheet(state: state)
        }
        .alert("Quit without submitting?", isPresented: $state.quitConfirmShowing) {
            Button("Cancel", role: .cancel) {}
            Button("Quit", role: .destructive) {
                Foundation.exit(1)
            }
        } message: {
            Text("All decisions will be lost.")
        }
        .toolbar {
            ToolbarItem(placement: .principal) {
                Picker("", selection: $state.groupBy) {
                    Text("Type").tag(GroupBy.type)
                    Text("Score").tag(GroupBy.score)
                    Text("Action").tag(GroupBy.action)
                    Text("File").tag(GroupBy.file)
                }
                .pickerStyle(.segmented)
                .help("Group findings by")
            }
            ToolbarItem(placement: .primaryAction) {
                Button {
                    state.attemptSubmit()
                } label: {
                    Label("Submit", systemImage: "checkmark.seal.fill")
                }
                .help("Submit decisions (⌘S)")
            }
        }
    }
}
