# review-triage — native macOS app

Companion to the `valian:review` skill: triages code-review findings produced by
the agents through a native macOS UI. Pendant of the Go TUI under
`cmd/review-triage/` (VAL-236), set to replace it once shipped (cf. VAL-238 AC 20).

## Local dev

The `.xcodeproj` is **generated** from `project.yml` by [XcodeGen](https://github.com/yonaskolb/XcodeGen) and is **not** committed.

```sh
brew install xcodegen
cd app/review-triage
xcodegen generate
open ReviewTriage.xcodeproj
```

Then build/run from Xcode (⌘R) or:

```sh
# Debug build + run (ad-hoc signed)
xcodebuild -project ReviewTriage.xcodeproj -scheme ReviewTriage -configuration Debug build

# Unit tests
xcodebuild test -project ReviewTriage.xcodeproj -scheme ReviewTriage -destination 'platform=macOS'
```

## Layout

```
app/review-triage/
├── project.yml                   # XcodeGen source of truth (versioned)
├── Configs/                      # xcconfig files (signing, deployment target, etc.)
│   ├── Shared.xcconfig
│   ├── App.xcconfig
│   ├── CLI.xcconfig
│   └── Tests.xcconfig
├── ReviewTriage/                 # GUI target — SwiftUI app
│   ├── Models/
│   ├── Views/
│   ├── Diff/
│   ├── Theme/
│   ├── Resources/Assets.xcassets/
│   └── ReviewTriage.entitlements
├── ReviewTriageCLI/              # CLI shim — embedded in Contents/MacOS/review-triage-cli
└── ReviewTriageTests/
```

The CLI target is built first and embedded inside the `.app` bundle by a post-build
script in the App target — so `Review Triage.app/Contents/MacOS/review-triage-cli`
is always in sync with the GUI binary.

## Distribution

Signed Developer ID + notarized via `notarytool` in CI
(`.github/workflows/release-app.yml`), published as a `.zip` GitHub release asset,
referenced by `Casks/review-triage.rb`. See the cask file and the workflow for the
exact tag / version sequencing.

## Invocation contract

Same JSON contract as the Go TUI (`schemaVersion: 1`, see [VAL-235](https://linear.app/valian/issue/VAL-235)). The CLI shim
forwards `--input`, `--output`, `--help`, `--version` to the .app.
