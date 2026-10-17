# homebrew-tools

A Homebrew tap for Valian's internal developer tools.

## Available tools

| Tool  | Description                                    | Install                             |
| ----- | ---------------------------------------------- | ----------------------------------- |
| [`frn`](#frn) | Fast `flutter run` launcher with device picker | `brew install valian-ca/tools/frn` |
| [`atelierd`](#atelierd-device) | Atelier dashboard daemon + mobile device bank | `brew install valian-ca/tools/atelierd` |

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
simulator/AVD clones that autonomous forge runs lease, release, and recycle
without stepping on each other — or on you.

```sh
atelierd device bank init [--ios N] [--android N]   # provision atelier-ios-1..N / atelier-android-1..N (default 2 + 2)
atelierd device lease --platform ios --session <id> # print a targetable UDID/serial alone on stdout
atelierd device release --session <id> [--platform ios|android]
atelierd device status                              # bank + physical devices, states, leases
```

- **`bank init`** is idempotent and sizes both ways: re-running tops up;
  shrinking deletes the free excess clones and never touches a leased one
  (warned, removed on a later pass). A machine missing one toolchain
  provisions the other side, warns, and exits 0.
- **`lease`** is non-blocking and idempotent per `(session, platform)`: a
  wedged device goes to recycling and the next one is tried; a cold device
  is booted. Exit codes: `0` leased, `10` bank exhausted or recycling-only,
  `11` bank not initialized. Connected physical devices are leasable too,
  after the virtual bank.
- **`release`** returns immediately — a virtual device erases and reboots in
  a detached background worker; a physical device is leasable again on the
  spot.
- **Lease lifecycle** — every `atelierd emit` and every `device` command
  carrying a session ID renews that session's leases; `hook:session-end`
  releases them; a lease unrenewed for ~1 h is lazily reaped; a free virtual
  device idle ~30 min is lazily shut down.
- **`frn` integration** — the picker suffixes bank devices with their lease
  state (e.g. `atelier-ios-1 — bank, leased, session abc123`) so a human
  avoids stepping on a running forge. frn never takes a lease.

State lives in `~/.atelier/devices.json`, guarded by a `flock` so concurrent
leases from parallel sessions never double-book a device.

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
