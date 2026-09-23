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
`fzf`, `yq` are fine to depend on — they're all in core Homebrew.

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

## Atelier Next campaign protocol

`internal/atelierd/next/` and `internal/atelierd/cmds/next.go` implement the isolated
`atelierd next` contract. The executable reference is `docs/atelier-next.md`; the
broader product/protocol proposal lives in the `claude-plugins` repository under
`plugins/atelier-next/PROTOCOL.md`. Keep command names, staging schemas, exit codes,
identity and recovery rules synchronized with those consumers when activating them.
Contract 3 / schema 2 includes the post-contribution user-trial gate, root QA,
repair integrations, a SHA-bound delivery gate, unique PR/CI observations, confirmed
delivery and suite. Deployment-only criteria remain explicitly unverified, with
justification/evidence and a matching manual deferred suite entry; tests/surfaces
and known defects cannot use this exception. CI plumbing repairs have kind=ci and
a durable maximum of three integrated rounds. The verification document is unique
per campaign. Old Next schemas are rejected, never silently migrated. No general state
setter, Linear API client, test runner, Git commit/push/merge or cloud event shipment
is performed by this book-keeper. It validates caller attestations, not their truth.

The `~/.atelier-next/` namespace is deliberate, independent of `~/.atelier/forge/`.
One namespace flock protects checkout uniqueness and atomic campaign snapshots;
one renewable session token controls the cooperative external editor. Tokens fence
CLI mutations, not arbitrary editor/Git writes, so expiration never authorizes
silent takeover. A prepared integration journals its exact parent/tree and evidence
before the caller invokes valian:commit. No campaign metadata belongs in commit
messages. Finish checks the one non-merge HEAD against the prepared parent/tree and
a clean bound checkout. Recovery reuses the journal's integration ID, never subject
matching, ticket footers, another commit, or automatic reset/recommit. Hooks changing
content, extra commits or unknown history require explicit reconciliation.
Linear Done synchronization is pending until acknowledged; standalone roots cannot
be acknowledged at contribution integration.

Next events are retained in the campaign snapshot as a local transactional journal.
Do not add them to the legacy cloud outbox/whitelist until the dashboard's schema
accepts them. Tests use temporary repositories and must never run Git against the
user's checkout. Run the full Go suite, vet, and race tests for Next/cmds when
changing the ownership or integration rules.

The target source release is 0.17.0. Its stable formula URL and checksum must come
from the actual tagged tarball after publication (per the release process above),
never from a guessed SHA or a version bump over the old tarball. The accompanying
formula change adds the HEAD contract test; the stable version bump is deferred to
that release step. The plugin skills remain gated until a compatible binary is
published; no local daemon replacement or restart is implied by implementing Next.

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

## CI

`.github/workflows/lint.yml` runs:
- `go vet ./...`, `go test ./...`, and `golangci-lint run` on every PR
- `shellcheck -x bin/*` when `bin/` isn't empty
- `brew audit --strict --online Formula/*.rb` if any formula changed

Keep all jobs mandatory via branch protection on `main`.

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
