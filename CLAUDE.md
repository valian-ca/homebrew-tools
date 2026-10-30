# homebrew-tools

A Homebrew tap for Valian's internal developer tools. Each tool here is a
small, focused utility — typically a shell script — that solves a friction
point in our daily workflows.

## Layout

```
homebrew-tools/
├── bin/                 # Bash scripts shipped by formulae (when bash fits)
├── cmd/<tool>/          # Go tool entrypoints (when Go fits)
│   └── main.go
├── internal/            # Go packages shared across cmd/* tools
├── Formula/             # One Ruby formula per source-built tool
│   └── frn.rb
├── go.mod / go.sum
├── .github/workflows/   # CI (go test, shellcheck, formula audit)
└── README.md
```

Install any tool with `brew install valian-ca/tools/<name>`.
`brew tap valian-ca/tools` is implicit on first install.

## Conventions for adding a new tool

**Language.** Bash is the default for small tools — cold-start latency is
zero and portability across the team's macOS machines is trivial. Reach for
Go when bash genuinely doesn't fit: parallel subprocess orchestration with
timeouts, non-trivial JSON, state machines, or anything over ~300 lines.
`frn` is an example — it lives under `cmd/frn/` with shared code in
`internal/`.

**Naming.** Short, memorable, **no `flutter-` prefix** — asdf's flutter
plugin shims anything matching that pattern and routes it to `flutter
<subcommand>`. Check the asdf shims directory before committing to a name.

**Dependencies.** Declare every runtime dependency via `depends_on` in the
formula. Assume the user has nothing beyond macOS defaults. `gum`, `jq`,
`fzf`, `yq` are fine to depend on — they're all in core Homebrew. A
formula from a third-party tap is allowed when the tool is in no core tap,
named fully: `lanes` depends on `f1bonacc1/tap/process-compose`. Homebrew
taps a third-party tap only for a formula named on the command line, never
for a dependency, so its install line names both:
`brew install f1bonacc1/tap/process-compose valian-ca/tools/lanes`.

**Scripts get `set -euo pipefail`** at the top. Use the
`${arr[@]+"${arr[@]}"}` idiom for optional arrays to stay compatible with
bash 3.2 (macOS default `/bin/bash`).

**No external config files required out of the box.** Tools should work with
zero configuration; any persistence goes under `.dart_tool/valian/` (for
Flutter projects) or `$XDG_CONFIG_HOME/valian/` (for general tools).

**`--help` is mandatory.** A `-h`/`--help` flag must print usage, options,
env vars, and a couple of examples. Use a `usage()` function and a `cat
<<EOF` heredoc.

## Adding a formula

Two patterns, depending on the implementation language.

**Bash tool** — installs the script directly:

```ruby
class Mytool < Formula
  desc "..."
  homepage "https://github.com/valian-ca/homebrew-tools"
  url "https://github.com/valian-ca/homebrew-tools/archive/refs/tags/mytool-0.1.0.tar.gz"
  sha256 "..."
  license "MIT"

  depends_on "gum"  # runtime deps as needed
  depends_on "jq"

  def install
    bin.install "bin/mytool"
  end

  test do
    assert_match "mytool", shell_output("#{bin}/mytool --help")
  end
end
```

**Go tool** — source-built via `go => :build` (see `Formula/frn.rb`):

```ruby
class Frn < Formula
  desc "..."
  homepage "https://github.com/valian-ca/homebrew-tools"
  url "https://github.com/valian-ca/homebrew-tools/archive/refs/tags/frn-0.3.0.tar.gz"
  sha256 "..."
  license "MIT"

  depends_on "go" => :build

  def install
    cd "cmd/frn" do
      system "go", "build", *std_go_args(ldflags: "-s -w -X main.version=#{version}")
    end
  end

  test do
    assert_match "frn", shell_output("#{bin}/frn --help")
    assert_match version.to_s, shell_output("#{bin}/frn --version")
  end
end
```

Go tools stamp their version via `-ldflags "-X main.version=..."`, and
a `--version` flag is expected. Runtime dependencies invoked via `exec`
(e.g. `adb`, `xcrun`, `flutter`) are assumed present on user machines
and don't need `depends_on`.

The formula name (filename + class) matches the binary name. Use
per-tool git tags like `frn-0.3.0` so multiple tools can version
independently.

## Release process

1. Make changes on a branch, PR, review, merge to `main`.
2. CI runs `go test ./...`, `shellcheck` on any `bin/*`, and
   `brew audit --strict` on changed formulae.
3. For Go tools, ensure `go test ./...` passes locally before tagging.
4. Tag the release: `git tag frn-0.3.0 && git push --tags`.
5. Create a GitHub release for the tag (optional but tidy — the tag alone is
   enough for brew to resolve the tarball URL).
6. Update the formula's `url`, `version` (implicit from the tag), and
   `sha256` in a separate commit. The new sha256 comes from
   `curl -sL <tarball-url> | shasum -a 256`.

Teammates pick up the update with `brew upgrade valian-ca/tools/<name>`.
Go tools are compiled on the user's machine at install time — the first
install pulls `go` as a build dependency, subsequent ones don't.

## Nerd Font glyphs

Some tools (`frn` at least) use Nerd Font glyphs from the Private Use Area
for device/platform icons. Go source files keep them as literal UTF-8
characters; editors occasionally strip these on write. If icons vanish,
verify with:

```
grep -R --include='*.go' --include='*.sh' -n '' internal/ bin/ cmd/ 2>/dev/null \
  | xxd | grep -iE 'ef 8[0-7]'
```

Expected bytes:
- `ef 85 bb` →  (android, U+F17B)
- `ef 85 b9` →  (apple, U+F179)
- `ef 80 a1` →  (rescan, U+F021)
- `ef 83 a2` →  (restart, U+F0E2)
- `ef 87 ab` →  (reconnect wireless, U+F1EB)

If they're gone, re-insert via a binary-safe write (Python,
`printf '\xee\x85\xbb'`, etc.) — not via every editor's UTF-8 "helpful"
normalisation.

## lanes test data

`lanes` is proven on the demo project in `examples/lanes-demo`, which follows
the contract in `cmd/lanes/README.md`. `examples/lanes-demo/make-repo.sh <dir>`
turns it into a throwaway repo with a bare `origin`; the integration suite
(`internal/lanes/integration`) builds one per test, with its own lanesd,
state dir and port range through the `LANES_TEST=1` overrides. No account is
needed.

The demo's Firebase side is the project `demo-lanes`: the `demo-` prefix needs
no credentials. Its cases need Java 21 and Node 22; without them they skip,
unless `LANES_REQUIRE_FIREBASE=1`, which CI sets. At most two Firebase stacks
run at once (`LANES_FIREBASE_PARALLEL` changes it), and the core tests run on
a copy of the demo without its Firebase side. Two more `LANES_TEST=1` seams
serve them: `LANES_TMPDIR_ROOT` moves the lanes' `TMPDIR` (the socket-length
refusal), and `LANES_LAN_PROBE=unreachable` makes `lanes host lan` see no
network.

The demo's MySQL side is a private `mysqld` that `tool/mysql-server <port>`
starts from the main checkout, its data under `.demo/mysql`, on port 3316 for
the demo; the integration suite gives each test its own server on an
ephemeral port and stops it by its pid file. Its cases need `mysqld`, from
`brew install mysql@8.4`: the formula is keg-only, and the scripts find it
through `brew --prefix mysql@8.4` when it is not on `PATH`. Without it they
skip, unless `LANES_REQUIRE_MYSQL=1`, which CI sets. Two more `LANES_TEST=1`
seams serve them: `LANES_MYSQL_WAKE_TIMEOUT` shortens the 2-minute wait for
a woken server, `LANES_RECONCILE_RETRY` the 5 minutes between two attempts
at an erase. Non-MySQL cases run on the demo turned back into a schema-1
`lanes.yaml`, which proves 0.3.0 still runs schema 1.

The seed (`examples/lanes-demo/seed`: 5 000 documents in `items`,
`demo/hello`, one Auth user) comes from a dedicated emulator session, never
from one tests ran against. To rebuild it, from `examples/lanes-demo`, on free
ports:

```
rm -rf seed
npx firebase emulators:exec --config firebase.emulator.json --project demo-lanes \
  --only auth,firestore --export-on-exit seed "node tool/make-seed.mjs"
```

## CI

`.github/workflows/lint.yml` runs:
- `go vet ./...`, `go test ./...`, and `golangci-lint run` on every PR
- `shellcheck -x bin/*` when `bin/` isn't empty, and on the lanes demo's
  bash scripts
- `brew audit --strict --online Formula/*.rb` if any formula changed
- `go test -tags integration` for lanes on the macOS runner, when a PR
  touches `cmd/lanes*`, `internal/lanes`, `examples/lanes-demo`,
  `Formula/lanes.rb`, `go.mod`, `go.sum` or the workflow. It needs process-compose and cgo
  (lanes reads processes through libproc), Java 21 and Node 22 for the
  Firebase cases, and `mysql@8.4` from brew for the MySQL cases; the
  emulators' jars are cached on the demo's firebase-tools version.

All four jobs are required checks on `main`. `.github/settings.yml` (the
Probot Settings app) declares that branch protection, along with the
repository's settings, labels and team access: a PR without an approving
review, the four jobs passing on a branch up to date with `main`, signed
commits, resolved conversations, admins included. Renaming a job in
`lint.yml` means renaming its check in `settings.yml` in the same PR, or
every PR waits on a check that never reports. The tap stays public
(`private: false`).

## When working with Claude Code in this repo

This tap is co-owned by the team. When asked to add or modify a tool:

- Read the existing tool(s) first to match conventions (for bash: shebang,
  flag parsing, help, error handling, `set -euo pipefail`; for Go: package
  layout under `cmd/<tool>/` and `internal/<tool>/...`, `--help` + `--version`
  flags, context-based timeouts for subprocesses).
- Don't introduce runtime dependencies without adding `depends_on` in the
  formula and updating this file's conventions list if it's a new kind of
  dep. Pure-Go libraries go in `go.mod` and don't need `depends_on`.
- Always update the implementation and `Formula/<tool>.rb` in the same PR.
- Bump the version in the formula when the tool changes — even trivially.
  Team members shouldn't have to think about whether `brew upgrade` is a
  no-op.
- Preserve PUA glyphs byte-for-byte if you touch code that uses them.
