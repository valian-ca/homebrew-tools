# review-triage — Project Brief

This document is a hand-off from a design session held in the `claude-plugins` repo. It captures the decisions taken and the open questions deferred, in a form suitable for feeding `atelier:blueprint` (to draft a Linear ticket) and then `atelier:plan-implement` / `atelier:build-implement` to actually build the binary.

## What it is

A Go TUI binary that helps a developer triage code-review findings — fix / skip / discuss — when invoked by the `valian:review` Claude Code skill. Takes findings as JSON, presents them in an interactive table with detail view and multi-select, returns decisions as JSON. The skill then executes the fix/skip decisions and continues the discussion in the LLM conversation for any `discuss` items.

## Why it exists

The current `valian:review` skill collects fix/skip/discuss decisions via an N-round LLM interview (one `AskUserQuestion` per finding). Two structural problems:

- **Latency**: each Q/A is a full LLM round-trip (~5–15 s). A 20-finding review = 2–5 minutes of passive wait while every detail is already known to the agents.
- **Granularity**: no batch primitive — the user cannot say "fix all comment-doctrine findings" in one stroke.

A native TUI moves the decision phase out of the LLM loop entirely. Zero LLM round-trips during decisions, syntax-highlighted code/diff in a real terminal, multi-select with group/score selectors, free-text prompt per `discuss` item. After the TUI exits, the LLM picks up the discussion prompts and executes the batch fix.

## High-level architecture

```text
┌─────────────────────────────┐       ┌──────────────────────┐
│  valian:review skill        │       │  review-triage       │
│  (Claude Code conversation) │       │  (Go binary, TUI)    │
│                             │       │                      │
│  1. agents find issues      │       │  - read input JSON   │
│  2. write input.json        │──────▶│  - present TUI       │
│  3. detect host emulator    │       │  - capture decisions │
│  4. spawn or instruct user  │       │  - write output JSON │
│  5. AskUserQuestion Done    │       │  - exit 0/1/2        │
│  6. read output.json        │◀──────│                      │
│  7. execute fixes + discuss │       └──────────────────────┘
└─────────────────────────────┘
```

The two processes communicate via JSON files in `/tmp/`. No long-lived process, no IPC, no daemon.

## JSON contract (schemaVersion 1)

### Input — written by the skill

```json
{
  "schemaVersion": 1,
  "branch": "feat/billing-csv",
  "mergeBase": "a3f9c1b",
  "findings": [
    {
      "id": 1,
      "title": "Journal comment in src/api.ts:L42",
      "group": "comments",
      "agentLabel": "#5: comments",
      "score": 90,
      "explanation": "The comment narrates a past change rather than explaining a non-obvious WHY. Per the doctrine, this is in scope retroactively.",
      "file": "src/api.ts",
      "lineStart": 40,
      "lineEnd": 44,
      "language": "typescript",
      "codeExcerpt": "const input = parseInput(req);\n// changed from foo to bar\nconst result = bar(input);\nreturn result;",
      "proposedFix": {
        "explanation": "Delete the comment.",
        "code": "const input = parseInput(req);\nconst result = bar(input);\nreturn result;"
      },
      "selection": "fix"
    }
  ]
}
```

**Field semantics:**

- `schemaVersion` (integer, required). Binary exits with code 2 on mismatch — lets us evolve the schema later without breaking older skill installs.
- `selection` (`"fix" | "skip" | "discuss" | null`, optional). Starting action. The skill's upstream LLM populates this with a heuristic (e.g. `fix` for high-confidence doctrine findings, `discuss` for ambiguous bugs, `skip` for low-score nitpicks). The TUI displays it as the default; the user adjusts. `null` means "no opinion, user must choose".
- `language`: chroma lexer name (`typescript`, `python`, `go`, `dart`, `tsx`, etc.).
- `lineStart` / `lineEnd`: 1-based, inclusive.
- `agentLabel`: the human-facing label used in the skill's existing table (e.g. `#5: comments`).
- `group`: the slug used for group-based multi-select (e.g. `comments`, `bugs`, `tests`).
- `codeExcerpt` may be empty when the finding suggests adding new code (no existing code to replace; rendered as all-`+` in the diff view). `proposedFix.code` may be empty when the fix is purely a deletion or behavioural change without a clean replacement snippet (rendered as all-`-`). The two MUST NOT be empty simultaneously — every finding must have at least one side to display.

### Output — written by the binary on exit 0

```json
{
  "schemaVersion": 1,
  "decisions": [
    { "id": 1, "action": "fix" },
    { "id": 2, "action": "fix" },
    { "id": 7, "action": "discuss", "discussPrompt": "Pourquoi pas un simple optional chaining ici ? Le throw casse l'API publique." },
    { "id": 8, "action": "skip" }
  ]
}
```

**Field semantics:**

- `action`: `"fix" | "skip" | "discuss"` — exactly one per finding.
- `discussPrompt`: present iff `action == "discuss"`. May be the empty string (user wants to discuss but has no preformed framing).
- Output MUST contain a decision for every finding present in the input.

### Exit codes

- `0` — output JSON written; skill reads it and continues.
- `1` — user cancelled (Ctrl-C, Esc-quit). No output JSON. Skill aborts.
- `2` — internal error (schema mismatch, malformed input, etc.). Details on stderr. Skill aborts and reports.

There is no `cancelled` field in the output JSON — exit codes carry that signal.

## TUI screens

Four screens. Three are full-screen views (Table, Detail, Discuss). Confirm is a small centered modal. On wide terminals (≥ 160 cols), Table and Detail render side-by-side as a master-detail split; below that threshold, Detail opens as a full-screen modal drilled into from the Table.

### Common keymap

Applies on every screen:

- `?` — open a contextual help overlay listing all keys for the current screen. `?` again or `Esc` to close.
- `Ctrl+S` / `Cmd+S` — global submit. Jumps to Confirm. Refused (with soft feedback in the footer) if any finding still has `action == null`.
- `Ctrl+C` / `q` (outside Discuss textarea) — quit. Opens a confirm modal ("Quit? All decisions will be lost. [y/N]"). On `y`, exits 1 without writing output JSON.
- `Esc` — context-dependent back/cancel; see per-screen sections.

### Screen 1 — Table

Entry screen. Lists all findings grouped by a configurable dimension. **Cursor-as-selection** model: there is no per-item checkbox state, the cursor IS the selection. Group headers are themselves actionable — an action key on a header applies to every item in the group.

**Group-by dimensions.** Toggle with `g`, cycling through:

1. **By type** (default) — uses each finding's `group` field (e.g., `comments`, `bugs`, `tests`).
2. **By score bucket** — bands such as `100 / <100 / ≤75 / ≤50 / ≤25 / 0` (exact bands tunable at implementation).
3. **By current action** — `?` (no decision yet), `fix`, `skip`, `discuss`. Crucial for end-of-triage: cycle here to find what's still undecided.
4. **By file** — group findings by source path.

Within a group, findings are sorted by `score` descending (high-confidence first).

**Group headers** are first-class rows showing the group name, count, and live action breakdown (e.g., `▼ comments (5)  3 fix · 1 discuss · 1 ?` — non-zero buckets only). They remain sticky to the top of the visible viewport when the cursor scrolls inside their group, so the group context stays visible.

**Keymap (Table):**

- `j` / `k` / `↑` / `↓` — move cursor (traverses items and headers as one flat list).
- `f` — set `action = fix`, advance to next row. On a header: set every item in the group to `fix`, advance to next header.
- `s` — set `action = skip`, advance to next row. On a header: set every item in the group to `skip`, advance to next header.
- `d` — on an item: open the Discuss textarea (Screen 3), preloaded with current `discussPrompt`; on save it commits `action = discuss` and advances. On a header: set every item in the group to `action = discuss` with empty `discussPrompt` (no textarea opens) and advance to next header. User can revisit items individually to fill prompts.
- `Tab` — accept the current `selection` (LLM-suggested pre-fill) and advance. Refused (no advance, soft feedback) if the item's `selection == null`. On a header: accept every item's `selection` in the group and advance to next header; items with `null` are silently left as `null`.
- `Enter` — drill into Detail (Screen 2) for the focused item. No drill action on a header. (In split mode at ≥ 160 cols, `Enter` becomes "focus right pane" instead — see master-detail section.)
- `g` — cycle group-by dimension (see above).

**Submit footer.** Persistent row at the bottom of the Table, always reachable by scrolling past the last item. Shows the live plan summary:

```
Submit  3 fix · 1 discuss · 1 skip · 0 ?
```

Reached automatically when `Tab` or an action key is pressed on the last row of the last group. `Enter` on Submit → Confirm screen.

### Master-detail split (width ≥ 160 cols)

On wide terminals, the Table renders at ~40% width on the left and the Detail at ~60% on the right. The Detail content live-updates as the Table cursor moves — no drill-in needed.

```
┌──────────────────────────────┬────────────────────────────────────────┐
│ Findings (group: types)      │ #5: comments  ·  src/api.ts:40-44      │
│                              │ score 90  ·  [FIX]                     │
│ ▼ comments (5)  3f·1d·1?     ├────────────────────────────────────────┤
│   #1  Journal comment   [F]  │ <explanation>                          │
│ ▶ #5  Journal comment   [F]  ├────────────────────────────────────────┤
│   #8  TODO without ...  [?]  │ <unified diff with chroma>             │
│   #11 Stale reference   [D]  ├────────────────────────────────────────┤
│ ▼ bugs (3)     2f·0d·1?      │ Proposed: <proposedFix.explanation>    │
│ ...                          │                                        │
│ Submit  3f·1d·1s·0?          │                                        │
├──────────────────────────────┴────────────────────────────────────────┤
│ [f/s/d] action  [Tab] keep  [j/k] nav  [Enter] focus right  [?] help  │
└───────────────────────────────────────────────────────────────────────┘
```

Semantic shifts in split mode:

- `Enter` no longer drills — instead it **focuses the Detail pane** (cursor moves to the right pane for scrolling long explanations or diffs). `Esc` returns focus to the Table.
- Action keys (`f` / `s` / `d` / `Tab`) continue to act on the Table cursor regardless of which pane has focus. The Table is the authoritative locus of decisions; the Detail pane is informative only.
- Detail viewport resets to top whenever the Table cursor changes finding.
- Discuss and Confirm always render as full-screen modal overlays even in split mode (a writing task and a confirmation, both demanding focus).
- Resize across the threshold is graceful: bubbletea's `WindowSizeMsg` triggers a re-layout each tick. Crossing 160→159 collapses split into modal mode (Detail closes, Table keeps cursor); crossing 159→160 opens split (Detail shows the current cursor finding).

### Screen 2 — Detail

Full-screen modal (width < 160) or right pane (width ≥ 160). Shows everything about one finding.

```
┌─────────────────────────────────────────────────────────┐
│ #5: comments  ·  Journal comment in src/api.ts:L42      │
│ src/api.ts:40-44  ·  score 90  ·  [FIX]                 │
├─────────────────────────────────────────────────────────┤
│ <explanation, scrollable viewport>                      │
├─────────────────────────────────────────────────────────┤
│   const input = parseInput(req);                        │
│ - // changed from foo to bar                            │
│   const result = bar(input);                            │
│   return result;                                        │
├─────────────────────────────────────────────────────────┤
│ Proposed: Delete the comment.                           │
├─────────────────────────────────────────────────────────┤
│ [f/s/d] action  [Tab] keep  [j/k] nav  [Esc] back       │
└─────────────────────────────────────────────────────────┘
```

**Action badge** in the header (`[FIX]` / `[SKIP]` / `[DISCUSS]` / `[?]`) reflects the current `action`. Colour-coded.

**Diff rendering.** Unified diff (`-`/`+` lines with coloured background) with chroma syntax highlighting applied per the finding's `language`. Computed line-by-line between `codeExcerpt` and `proposedFix.code`.

- Both populated → standard unified diff.
- `proposedFix.code` empty → diff is all-`-` (pure deletion).
- `codeExcerpt` empty → diff is all-`+` (pure addition).

**Parity with `output-format.md`.** The Detail view reproduces the section structure of the existing LLM-rendered per-issue output in `valian:review` so users don't have to re-orient between the two contexts. Same field labels, same ordering, same emphasis.

**Keymap (Detail):**

- `j` / `k` / PgUp / PgDn / Ctrl+U / Ctrl+D — scroll the viewport (when Detail has focus in split mode, or always in modal mode).
- `g` / `G` — top / bottom of the viewport. (Note: `g` conflicts with the Table's group-by toggle; in Detail, `g` is unambiguously top-of-viewport because group-by only applies to the Table.)
- `→` / `l` or `←` / `h` — navigate to next / previous finding (without returning to Table). Respects current group-by ordering.
- `f` / `s` / `d` — set action and advance to next finding (same auto-advance semantics as Table). On `d` for an already-`discuss` item, opens the Discuss textarea to edit the prompt.
- `Tab` — accept current and advance.
- `Esc` / `q` — return to Table at the same cursor position. In split mode (focused Detail pane), returns focus to Table.

### Screen 3 — Discuss

Full-screen modal even when Table+Detail are in split mode. Captures or edits a `discussPrompt`.

```
┌─────────────────────────────────────────────────────────┐
│ #7: bugs  ·  Throws on null reference in handleClick    │
│ src/handlers.ts:88-92                                   │
│                                                         │
│   if (user.profile.avatar.url) { ... throw ... }        │
├─────────────────────────────────────────────────────────┤
│ Pourquoi pas un simple optional chaining ici ? Le      │
│ throw casse l'API publique.█                            │
│                                                         │
│                                                         │
├─────────────────────────────────────────────────────────┤
│ [Tab] save  [Esc] cancel  [Enter] newline               │
└─────────────────────────────────────────────────────────┘
```

**Header.** Compact context: finding title, file:line, and ~5 lines of the `codeExcerpt` for reference. No diff — the user is in writing mode, not analysis mode.

**Textarea.** `bubbles/textarea`, multi-line, plain text (no markdown rendering, no vim keybindings in V1). Preloaded with current `discussPrompt` value if non-empty. Native viewport scroll for long content.

**Keymap (Discuss):**

- `Enter` — insert newline (standard textarea semantics).
- `Tab` — save and advance: commit `action = discuss` with the typed prompt, return to caller (Table or Detail) at the next finding.
- `Esc` — cancel. If opened fresh (item was not `discuss` before), the `d` keystroke is rolled back entirely — `action` reverts to its previous value (`null`, `fix`, `skip`, or whatever). If opened for editing (item was already `discuss`), the existing prompt is preserved unchanged.

### Screen 4 — Confirm

Small centered modal. Pre-exit sanity check.

```
              ┌──────────────────────────────────┐
              │  Plan                            │
              │                                  │
              │    3 fix                         │
              │    1 discuss  (1 sans prompt)    │
              │    1 skip                        │
              │                                  │
              │  [Enter] ship   [Esc] back       │
              └──────────────────────────────────┘
```

**Refusal of submit if `?` remaining.** Already enforced upstream by the Submit footer and `Ctrl+S` / `Cmd+S` handlers — Confirm should never be reached with undecided items. Defensive: if somehow it is, Confirm displays "N findings still undecided" and refuses `Enter`.

**Soft warning for empty discuss prompts.** Non-blocking. The contract allows `discussPrompt: ""`, but the user may have forgotten to fill prompts left empty by group-level `d` bulk-actions. Confirm shows the count parenthetically (`1 discuss (1 sans prompt)`). User can `Esc` back, navigate via group-by "by current action" → `discuss` group, and fill them — or accept and ship as-is.

**Keymap (Confirm):**

- `Enter` — write output JSON to the `--output` path, exit 0.
- `Esc` — return to Table at the previous cursor position.

### Cross-screen behaviour notes

- **Vertical scroll** is provided via `bubbles/viewport` in three places: the Table (with sticky group headers), the Detail (whether in modal or split-pane form), and the Discuss textarea (built into `bubbles/textarea`). The Detail viewport resets to top whenever the underlying finding changes; the Table and Discuss preserve their scroll positions.
- **Theme.** `lipgloss` auto-adapts to dark/light terminal background. Respects `$NO_COLOR` per https://no-color.org — all colour is replaced by text-only markers (`-` / `+` for diff, plain `[FIX]` / `[SKIP]` / `[DISCUSS]` / `[?]` badges with no background).
- **Width.** Soft floor — the binary renders at any width but degrades gracefully below ~80 cols (line wrapping in viewport content, footer hints truncated). Master-detail split engages at ≥ 160 cols.
- **Mouse support.** Not in V1 (keyboard only).

## Stack

- **Go ≥ 1.22** (matches the rest of the monorepo)
- `github.com/charmbracelet/bubbletea` — TUI framework (Elm-style MVU)
- `github.com/charmbracelet/bubbles` — table, textarea, viewport components
- `github.com/charmbracelet/lipgloss` — styling and layout primitives
- `github.com/alecthomas/chroma/v2` — syntax highlighting (~250 languages)
- Stdlib `flag` for CLI args (escalate to `spf13/cobra` only if scope expands)

All four Charm libraries plus chroma are added to the **shared `go.mod`** of `homebrew-tools`. No impact on other tools at build time (each `go build cmd/<tool>` only links what it actually imports), but `go.sum` grows for everyone.

## Repo placement (inside the existing `homebrew-tools` monorepo)

`review-triage` lives in the same Go monorepo as `atelierd` and `frn` — see `~/code/valian/homebrew-tools/CLAUDE.md` for the conventions. No new repo is created.

### Files to add

```text
~/code/valian/homebrew-tools/
├── cmd/
│   └── review-triage/
│       └── main.go              ← entry point: arg parsing, JSON I/O, bubbletea bootstrap
├── internal/
│   └── review-triage/           ← TUI components, parsing, rendering
│       ├── contract/            ← Go types mirroring the JSON schema
│       ├── tui/                 ← bubbletea models & views (table, detail, discuss, confirm)
│       └── highlight/           ← thin wrapper around chroma for code excerpts
├── Formula/
│   └── review-triage.rb         ← new formula (copy frn.rb / atelierd.rb structure)
└── go.mod                       ← updated with Charm + chroma deps
```

### Build & distribution pattern (same as `frn` and `atelierd`)

- **Source-built at install time** via `depends_on "go" => :build` in the formula. No precompiled binaries, no `goreleaser`, no separate release artifacts. The user's `brew install` triggers `go build` on their machine.
- **Version stamping** via `-ldflags "-X main.version=#{version}"`. The binary must expose `--version`.
- **`--help` flag mandatory** per the repo's conventions.
- **Per-tool git tag** for releases: `review-triage-0.1.0`, `review-triage-0.2.0`, etc. The tag drives the tarball URL the formula points at.
- **Formula** follows the `frn.rb` template exactly (Go monorepo subdirectory build with `std_go_args`).

### Install command

`brew install valian-ca/tools/review-triage` — same tap as `atelierd` and `frn`. No new tap.

### Release workflow

Identical to the existing pattern documented in `homebrew-tools/CLAUDE.md` (release process section):

1. PR → review → merge to `main`.
2. CI (`go test ./...`, `golangci-lint run`, `brew audit --strict`) runs on every PR.
3. `git tag review-triage-0.1.0 && git push --tags`.
4. Update `Formula/review-triage.rb`'s `url` (tarball pointing at the new tag) and `sha256` in a separate commit.
5. Teammates: `brew upgrade valian-ca/tools/review-triage`.

## Integration with the `valian:review` skill

### Hard constraint: Claude Code's Bash tool has no TTY

Claude Code executes bash commands non-interactively with no stdin attached — see `anthropics/claude-code#26353`. A bubbletea TUI launched directly via the Bash tool is not usable: it renders without keyboard input forwarding. The skill therefore arranges for the TUI to run **outside** the Bash-tool subprocess.

### Invocation pattern

The skill detects the host terminal emulator via `$TERM_PROGRAM` (and `$WEZTERM_UNIX_SOCKET` for the WezTerm case), then takes one of two branches:

**WezTerm — auto-spawn.** The skill executes via Bash:

```bash
wezterm cli spawn --new-window -- review-triage \
  --input /tmp/rt-in-<sha>.json \
  --output /tmp/rt-out-<sha>.json
```

WezTerm opens a new window with the TUI attached and returns control to the skill immediately. The new-window choice (over new-tab or split-pane) gives the TUI full screen real estate — important for the master-detail split, which engages at ≥ 160 cols and benefits from the widest available terminal. The user does not have to do anything to launch the TUI.

**Any other emulator (iTerm, Terminal.app, VSCode/Cursor integrated terminal, plain ssh, …)** — print the command and let the user paste it in a new tab/pane themselves:

```bash
review-triage \
  --input /tmp/rt-in-<sha>.json \
  --output /tmp/rt-out-<sha>.json
```

No per-emulator instructions are printed — Valian developers are expected to know how to open a terminal in their environment. Future work may add auto-spawn for additional emulators if a clean CLI path exists (iTerm and Terminal.app would require `osascript`, which is brittle; VSCode has no documented CLI for "open a new terminal pane").

### Reintegration: `AskUserQuestion` Done / Cancel

In both branches the skill immediately follows the spawn/print with a single `AskUserQuestion`:

- Header: `Triage`
- Question: `Click Done when the TUI has exited.`
- Options:
  - `Done` — skill reads the output JSON and continues to step 6 (fix execution + discuss residue handling).
  - `Cancel` — skill aborts.

The `Done` choice does not implicitly trust that the TUI exited successfully — the skill checks for file existence and validates `schemaVersion` on read; if anything is off, it reports the error and stops.

### Fallback: legacy path if binary missing

The skill first runs `command -v review-triage`. If the binary is absent, it falls back to the existing legacy interview (the current step 5 in `valian:review/SKILL.md` — one-by-one `AskUserQuestion` per finding). This preserves usability for anyone who has not installed `review-triage` yet, and makes the rollout incremental.

### Skill frontmatter additions

The `valian:review` skill will need these additions to its `allowed-tools`:

- `Bash(command -v review-triage)` — detection
- `Bash(wezterm cli spawn *)` — WezTerm auto-spawn branch

`Read` and `AskUserQuestion` are already present. There is intentionally no `Bash(review-triage *)` because the binary runs out-of-process (either via WezTerm CLI or pasted by the user in a separate terminal); the skill never invokes the binary directly via Bash.

## Decisions deferred to V2 / implementation time

These were considered and intentionally pushed out of V1 scope:

- **Persistence between launches.** V1 is one-shot — Ctrl+C means exit 1 and no decisions are persisted. Future work could cache partial decisions for resume-on-retry.
- **Discuss textarea — shell-out to `$EDITOR`.** V1 uses inline `bubbles/textarea`, multi-line. Shell-out to `$EDITOR` (à la `git commit`) was considered but rejected for V1 because it breaks the TUI keyboard control during the editor session and interacts awkwardly with WezTerm's new-window invocation. Reconsider in V2 if inline writing proves frustrating for long prompts.
- **Discuss markdown rendering / vim keybindings.** Plain text only in V1. The LLM reads `discussPrompt` verbatim, so users can write markdown if they want — but no rendering, no vim mode.
- **Mouse support.** Not in V1. Keyboard only. Add in V2 if requested.
- **Score-bucket exact bands.** The group-by-score view uses bands such as `100 / <100 / ≤75 / ≤50 / ≤25 / 0`. Final bands tunable at implementation based on the score distribution seen in practice.
- **Exact colour palette.** Chosen at build time with a preview pass. Standard `lipgloss` palette with `NO_COLOR` fallback.
- **Group header collapse/expand.** `Enter` on a header is currently a no-op. Adding collapse/expand semantics would be a natural V2 enhancement once the base TUI is settled.
- **V2 — detached-daemon mode.** Alternative invocation where the binary runs detached (via `dtach` or in-Go PTY handling) and the user attaches from any terminal with a single `review-triage attach <sock>` command. Implement only if the V1 invocation pattern (WezTerm auto-spawn + manual paste elsewhere) proves frictionful in real use.

## Design alternatives considered and rejected

For future-proofing — these were evaluated during the design session and dropped, but might come back if V1 hits limits:

- **Per-emulator dispatcher** (osascript for iTerm + Terminal.app + WezTerm CLI + something for VSCode): rejected as too much OS-specific glue to maintain across 4 emulators with the diversity in the Valian team.
- **In-LLM batch decision question with free-form grammar parsing** (a single `AskUserQuestion` with `fix: 1-4 ; skip: 5,6 ; discuss: rest` syntax): designed and prototyped in the `valian:review` skill, then reverted in favour of the TUI direction — the LLM round-trip cost was lower but the syntax was awkward for `discuss` prompt capture and the user could not see proposed fix details before committing to a decision.
- **MCP PTY server** (`so2liu/pty-mcp`): rejected because it puts Claude in the driver's seat of the PTY, defeating the goal of giving the human direct control of the TUI.
- **Standalone repo** (`github.com/valian-ca/review-triage`): rejected in favour of joining the existing `homebrew-tools` monorepo — same Go module, same CI, same release process, no new tap.

## How to use this document in the next session

1. Open a new Claude Code session with cwd `~/code/valian/homebrew-tools/`.
2. Hand this brief (`docs/review-triage-bootstrap.md`) to `atelier:blueprint` to draft a Linear ticket (or several — there are at least three natural sub-deliverables: the Go binary itself under `cmd/review-triage`, the `Formula/review-triage.rb` formula, and the `valian:review` skill integration in a separate PR on the `claude-plugins` repo). The blueprint should produce acceptance criteria covering the JSON contract, the four screens, the WezTerm auto-spawn detection, the manual-paste path, the skill fallback path, the formula, and the per-tool tag/release flow.
3. After the ticket(s) exist, hand off to `atelier:plan-implement` for the implementation plan, then `atelier:build-implement` to actually code.

## Cross-references

- **`homebrew-tools` conventions** — `~/code/valian/homebrew-tools/CLAUDE.md` (Go layout, formula patterns, release workflow, naming rules).
- **`atelierd`** — `~/code/valian/homebrew-tools/cmd/atelierd/` + `Formula/atelierd.rb`. Closest structural sibling: Go monorepo tool used by Claude Code skills via Bash.
- **`frn`** — `~/code/valian/homebrew-tools/cmd/frn/` + `Formula/frn.rb`. Reference for the formula template used by source-built Go tools in this monorepo.
- **`valian:review` skill** — `~/code/valian/claude-plugins/plugins/valian/skills/review/SKILL.md`. The integration target.
- **`valian:review` output format** — `~/code/valian/claude-plugins/plugins/valian/skills/review/references/output-format.md`. Defines the existing per-issue rendering; useful for parity reference.
- **Claude Code TTY limitation** — `https://github.com/anthropics/claude-code/issues/26353`.
