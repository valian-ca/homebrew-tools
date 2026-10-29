# homebrew-tools

A Homebrew tap for Valian's internal developer tools.

## Available tools

| Tool  | Description                                    | Install                             |
| ----- | ---------------------------------------------- | ----------------------------------- |
| [`frn`](#frn) | Fast `flutter run` launcher with device picker | `brew install valian-ca/tools/frn` |
| [`atelierd`](#atelierd-device) | Atelier dashboard daemon + mobile device bank | `brew install valian-ca/tools/atelierd` |
| [`lanes`](#lanes) | A local stack per git worktree, run by the `lanesd` daemon | `brew install f1bonacc1/tap/process-compose valian-ca/tools/lanes` |

## Install the tap

```sh
brew tap valian-ca/tools
```

The `homebrew-` prefix is dropped from the tap name by Homebrew convention.

## Upgrading

```sh
brew update
brew upgrade valian-ca/tools/<name>
```

## Session role

A Claude Code session declares its role to the atelier dashboard through
`atelierd`:

```sh
atelierd emit session:role "$CLAUDE_CODE_SESSION_ID" --data role=vigie
```

- **Admitted values** — `vigie` (a long-running session that keeps a
  project's plan and follows its ticket sessions) and `none` (the session
  is an ordinary one again). Values are case-sensitive; any other value, or
  no `role` at all, makes `atelierd emit` fail without writing to the
  outbox.
- **The last declaration wins, for the whole session.** A session may
  declare itself at any time and as often as it likes; the dashboard keeps
  the latest declaration and applies it to the entire session, from its
  first minute.
- **A vigie never runs an atelier skill in its own session.** Blueprint,
  plan-implement, forge and the other skills run in separate sessions. If
  a vigie runs one anyway, the dashboard shows it like any other session:
  the ticket card appears and the phase's time goes to the ticket.

Like every event, the declaration goes through the outbox: it needs neither
network nor authentication when it is emitted.

### If you run a vigie

Until a dedicated vigie plugin exists, apply the contract through your
vigie's own instructions. Put this command in its startup instructions:

```sh
atelierd emit session:role "$CLAUDE_CODE_SESSION_ID" --data role=vigie
```

And copy these two rules into them:

- Never run an atelier skill in the vigie session.
- If the session stops being a vigie, declare it again with `role=none`:
  `atelierd emit session:role "$CLAUDE_CODE_SESSION_ID" --data role=none`.

---

## `frn`

Fast `flutter run` launcher with device picker. Implemented in Go; `brew
install` compiles it from source (pulls `go` as a build dependency).

![frn in action](docs/frn-demo.gif)

### Why frn?

- **Fast.** Device picker pops in ~200 ms vs `flutter run`'s ~7 s before it even shows devices.
- **Remembers your choice.** Last used flavor + device are persisted per-project in
  `.dart_tool/valian/frn.json` — re-runs are one `<enter>`.
- **Flavors done right.** Auto-detects Android `productFlavors`, iOS schemes, and
  `lib/main_*.dart` entry points. No more forgetting `--flavor development`.
- **VM service URI auto-wired.** Writes to `.dart_tool/valian/vmservice.uri` so other Valian
  tooling can auto-attach — no manual `--vmservice-out-file` flag.

---

## `atelierd device`

The machine-local source of truth for mobile-device attribution: a bank of
simulator/AVD clones, plus the physical devices plugged by cable, that
autonomous forge runs lease, release, and recycle without stepping on each
other — or on you.

```sh
atelierd device bank init [--ios N] [--android N]   # provision atelier-ios-1..N / atelier-android-1..N (default 2 + 2)
atelierd device bank limit [N]                      # print or set how many virtual devices may be on at once (default 1)
atelierd device lease --platform ios --session <id> [--type any|virtual|physical] [--app-id <id>] [--wait 15m] [--json]
                                                    # print a targetable UDID/serial alone on stdout (or the lease as JSON)
atelierd device pause --session <id> [--platform ios|android]
                                                    # power the session's virtual devices off, keep the lease and the data
atelierd device prepare --session <id> --platform android [--reverse tcp:<port>]... [--clear] [--grant <permission>]...
                                                    # adb reverse, pm clear, pm grant on the leased phone and its app id only
atelierd device release --session <id> [--platform ios|android]
atelierd device status                              # bank + physical devices, states, ids, leases, queue
atelierd device restore <id> [--forget]             # retry a physical device's pending restore, or drop it
```

- **`bank init`** is idempotent and sizes both ways: re-running tops up;
  shrinking deletes the idle excess clones and keeps a leased or recycling
  one (warned, removed on a later pass). Every unleased AVD in range is
  recreated on the current device profile, a `broken` device is repaired
  and leasable again, and the `bank limit` setting is kept. The Android
  side holds at most 16 AVDs (adb discovers console ports 5554–5584 only).
  A machine missing one toolchain provisions the other side, warns, and
  exits 0.
- **States** — a virtual device is `off`, `on`, `recycling` or `broken`.
  `status` lists one row per device with `NAME PLATFORM TYPE STATE ID
  LEASE`: ID is the targetable id (`flutter run -d`), followed by the
  serial when it differs; a physical device is named without its transport
  and listed once, by its USB transport, even when adb also sees it over
  wifi. The LEASE column always starts with the holding session's id
  (`session S, on since 15:04, renewed 12s ago` · `session S, suspended
  since 15:20` · `session S, device died — restarts at next lease`); a
  physical device shows `not leasable: wireless only` or `restore pending
  since 15:04 (<last error>)` when that applies.
- **Ignition limit** — at most `bank limit` virtual devices are on at the
  same time, all sessions and platforms together (default `1`: a running
  AVD takes ~9 GB). A suspended lease does not count; a recycling device
  counts until its power-off is confirmed.
- **`lease`** is idempotent per `(session, platform)`. It powers a device
  on within the limit and in arrival order: when it cannot, the session
  waits in the queue up to `--wait` (default `0`) and exits `10`. The queue
  keeps a session's place across calls and forgets it 2 min after its last
  call's deadline, or at once when the wait is interrupted (Ctrl-C,
  SIGTERM); `status` shows it, the holder of a powered-on device gets a
  warning on stderr from its `atelierd device` and `atelierd forge` commands
  that succeed, and after 10 min of waiting one macOS notification is
  posted. A device that fails to boot goes to recycling; it keeps counting
  toward the limit until its power-off is confirmed, so another device is
  tried as soon as the limit allows, within `--wait`.
  Exit codes: `0` leased, `10` not served within the wait (bank exhausted,
  limit reached, or no physical device available under `--type physical`),
  `11` bank not initialized.
- **Physical devices** — `--type` says which devices a lease admits:
  `any` (default), `virtual`, or `physical`. Under `any` the lease returns
  a physical device plugged by cable and free first, then a virtual device
  through the queue; under `physical` it never queues and exits `10`.
  Without `--app-id`, no new physical device is leased, whatever `--type`
  says. A held lease whose kind `--type` no longer admits ends as `release`
  ends it, and the choice is made again: `--type virtual` always returns a
  virtual device. A suspended virtual lease gives way to a free physical
  device when `--type` admits one and `--app-id` is given — the virtual
  device is recycled — while a powered-on one is kept. A physical device
  is available when it is plugged by USB with debugging allowed on this
  machine; one reachable only without cable (adb wifi, Wi-Fi pairing to
  Xcode) is skipped, since its screen locks and its connection can drop.
  Physical devices do not count toward the ignition limit, and the lease
  powers nothing on. Leasing one needs `--app-id`, the run's application
  id: when that app is already installed and is neither a developer build
  nor named `[D] …` (a Store build, say), the device is unavailable for the
  lease and nothing on it is touched — `flutter run` would uninstall it with
  its data. A developer build or a `[D] …` app may be overwritten; when it
  was signed on another machine, `flutter run` reinstalls it and its dev
  data is lost. Each skipped device is named on stderr — except while a
  suspended virtual lease is checked for a physical device to give way to,
  when only a device refused for its app or its reads is named; stdout
  stays the id alone.
- **`lease --json`** prints the lease as one JSON object on one line
  instead of the bare id:
  `{"id":"…","name":"iPhone 12 Mini","serial":"…","platform":"ios","physical":true,"appId":"…","freshInstall":true}`.
  `id` is the targetable id, `serial` the one `status` shows. Nothing is
  printed on stdout when the lease fails.
- **Fresh install** — `freshInstall` is `true` when `--app-id` was not
  installed on the physical device as the lease began, and stderr then
  carries a line starting with `fresh install: `. The app's first launch
  raises the system permission alerts, among them the iOS Local Network
  alert, which a test driver cannot see and which cuts the app off from the
  Firebase emulators reached over the Mac's LAN IP until it is accepted.
  The signal belongs to the lease: every call of the same lease repeats it,
  pauses included; a new lease, after a release, finds the app kept and
  does not. It is always `false` on a virtual device.
- **On a physical device** the bank never erases, resets, powers off or
  recycles anything. An Android device gets `stay_on_while_plugged_in` set
  to `3` for the lease, after its previous value and installed packages are
  recorded; nothing else is changed. An iPhone is left as it is (set its
  Auto-Lock to Never by hand).
- **`prepare`** acts on the physical Android device the session leases, and
  on its lease's `--app-id` only: `--reverse tcp:<port>` reverses that port
  to the same port on the Mac (the return removes every reverse), `--clear`
  runs `pm clear` on the lease's app id, `--grant <permission>` runs `pm
  grant` for it. No other device or package can be reached through it, so
  a skill can allowlist `atelierd device *` rather than raw `adb`, whose
  wildcards would approve any shell command. A missing, virtual, iOS or
  suspended lease does nothing and fails.
- **`pause`** suspends the session's leases — or only the one of
  `--platform`: each virtual device is powered
  off with its installed app and data kept, and the session's next `lease`
  powers the same device back on (through the queue when the limit is
  reached), unless a free physical device takes its place. A physical
  Android device gets back its `stay_on` value, set to `3` again by the
  next `lease`; a physical iPhone is left as it is. A
  physical device unplugged meanwhile loses the lease, and the next `lease`
  chooses again.
- **`release`** returns immediately — a detached worker powers the virtual
  device off and erases it **without booting it**: it is `off` again within
  5 min (an AVD is wiped by `-wipe-data` on its next boot). A recycle that
  fails twice leaves the device `broken`, its reason shown by `status`, and
  never leased again until `bank init` repairs it. A physical device is
  restored by a detached worker instead: on Android its `stay_on` value
  from before the lease is put back (unless the device already holds it)
  and its `adb reverse` rules removed. No app is ever uninstalled, the
  run's `--app-id` build included: an install from scratch resets the iOS
  permissions (Local Network, location, notifications), while the next run
  installs over the kept build and keeps them. An iPhone has nothing to put
  back and is leasable again at once. An Android device is leasable again
  once the restore succeeds; an unreachable one keeps its restore pending,
  retried by later `device` commands once it is connected again, by any
  transport. `atelierd device restore <id> --forget` drops a restore that
  cannot succeed (a phone that forbids writing the setting) without
  touching the device.
- **AVDs start with 4 GB of RAM** (`-memory 4096`), whatever they were
  provisioned with.
- **Lease lifecycle** — every `atelierd emit` and every `device` command
  carrying a session ID renews that session's leases; `hook:session-end`
  releases them; a lease unrenewed for ~1 h, suspended or not, is reaped.
  Every `device` command except `bank limit` also checks the recorded state
  against the simulators and emulators actually running: a device stopped
  by hand is `off` (a leased one restarts at the session's next lease), and
  a virtual device on without a lease is powered off. There is no resident
  daemon: these checks run when a command runs, and the only background
  processes are the detached recycle and restore workers a release spawns,
  bounded to 5 min.
- **`frn` integration** — the picker suffixes bank devices with their lease
  state (e.g. `atelier-ios-1 — bank, on, session abc123 · def456 waiting 12
  min`) so a human avoids stepping on a running forge. frn never takes a
  lease.

State lives in `~/.atelier/devices.json`, guarded by a `flock` so concurrent
leases from parallel sessions never double-book a device. Every state
change is appended to `~/.atelier/device-transitions.jsonl`, one JSON line
with `at`, `device`, `platform`, `from`, `to`, `session`, `reason`, and the device's
`on` / `leased` flags after the change. This lists every stretch a virtual
device spent on without a lease for more than 2 min:

```sh
jq -rs 'def ts: sub("\\.[0-9]+"; "") | fromdateiso8601; group_by(.device)[] | sort_by(.at) | . as $t | range(0; length) as $i | select($t[$i].on and ($t[$i].leased | not)) | ((if $i + 1 < ($t | length) then ($t[$i + 1].at | ts) else now end) - ($t[$i].at | ts)) as $s | select($s > 120) | "\($t[$i].device) on without lease for \($s | floor)s from \($t[$i].at)"' ~/.atelier/device-transitions.jsonl
```

A `reap-unleased-on` line marks a device found on without a lease and
powered off.

## `lanes`

`lanes` gives each git worktree of a project its own local stack — a block of
ports, a short `TMPDIR`, the project's resources and processes — run by
process-compose under the `lanesd` daemon. A stack stops when no Claude
session works in its worktree any more; its data stays until the worktree
goes away. It serves worktrees only: a project keeps working on its main
checkout without it.

```sh
brew install f1bonacc1/tap/process-compose valian-ca/tools/lanes
brew services start lanes

cd <a worktree of a project with lanes.yaml>
lanes start          # start the stack, wait until it is ready
lanes list           # every lane on this Mac
lanes stop           # stop it, keep its data
```

A project adopts lanes with a `lanes.yaml`, a `process-compose.yaml` and
three hooks. The contract is in [`cmd/lanes/README.md`](./cmd/lanes/README.md),
and [`examples/lanes-demo`](./examples/lanes-demo) follows it
(`examples/lanes-demo/make-repo.sh <dir>` turns it into a throwaway repo).

## Contributing

See [CLAUDE.md](./CLAUDE.md) for conventions when adding a new tool or
modifying an existing one — naming rules, dependency policy, bash
compatibility notes, and the release process.

### Release process (short form)

1. Commit changes to `bin/<tool>` (and any formula updates) on `main`.
2. Tag a release using a per-tool prefix:
   ```sh
   git tag <tool>-0.1.1
   git push --tags
   ```
3. Compute the tarball's sha256:
   ```sh
   curl -sL https://github.com/valian-ca/homebrew-tools/archive/refs/tags/<tool>-0.1.1.tar.gz \
     | shasum -a 256
   ```
4. Update the formula's `url` and `sha256` in a follow-up commit.
5. Teammates run `brew upgrade valian-ca/tools/<tool>`.

## CI

Every PR and push to `main` runs:

- `go vet ./...`, `go test ./...`, `golangci-lint run` — for Go packages.
- `shellcheck -x bin/*` — when `bin/` contains scripts.
- `brew audit --strict --online valian-ca/tools/<name>` — only when a formula changes.
- `go test -tags integration` for lanes, on macOS — only when lanes or the Go modules change.
