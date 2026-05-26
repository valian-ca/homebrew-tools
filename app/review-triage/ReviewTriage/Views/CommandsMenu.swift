import SwiftUI

struct CommandsMenu: Commands {
    let state: TriageState?
    // Independent @AppStorage observer of the same key as ReviewTriageApp —
    // SwiftUI keeps the two in sync via UserDefaults, so the picker reflects
    // and drives the App's preferredColorScheme without an explicit binding.
    @AppStorage(AppTheme.defaultsKey) private var appTheme: AppTheme = .auto

    var body: some Commands {
        CommandMenu("Action") {
            Button("Fix") { dispatch(.fix) }
                .keyboardShortcut("f", modifiers: .command)

            Button("Skip") { dispatch(.skip) }
                .keyboardShortcut("k", modifiers: .command)

            Button("Discuss…") { state?.openDiscussForCurrentSelection() }
                .keyboardShortcut("d", modifiers: .command)

            Divider()

            Button("Accept Suggestion") {
                if let id = state?.selectedRowID {
                    state?.acceptSuggestion(forRowAt: id)
                }
            }

            Divider()

            // ⌘S (Save → Submit) instead of ⌘↩ because ⌘↩ is commonly bound
            // by Spotlight / Raycast / Alfred — interferes with user launchers.
            Button("Submit…") { state?.attemptSubmit() }
                .keyboardShortcut("s", modifiers: .command)
        }

        CommandMenu("View") {
            Picker("Group By", selection: groupByBinding) {
                Text("Type").tag(GroupBy.type)
                Text("Score").tag(GroupBy.score)
                Text("Action").tag(GroupBy.action)
                Text("File").tag(GroupBy.file)
            }
            Button("Cycle Grouping") { state?.cycleGroupBy() }
                .keyboardShortcut("g", modifiers: .command)

            Divider()

            Picker("Theme", selection: $appTheme) {
                ForEach(AppTheme.allCases) { theme in
                    Text(theme.label).tag(theme)
                }
            }
        }
    }

    private func dispatch(_ action: Action) {
        guard let state, let id = state.selectedRowID else { return }
        switch id {
        case .item(let findingIdx):
            state.apply(action, toFindingAtIndex: findingIdx)
        case .header:
            state.applyToGroup(action, headerKind: id)
        case .submit:
            break
        }
    }

    private var groupByBinding: Binding<GroupBy> {
        Binding(
            get: { state?.groupBy ?? .type },
            set: { state?.groupBy = $0 }
        )
    }
}
