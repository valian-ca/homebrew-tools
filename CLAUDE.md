# homebrew-tools

A Homebrew tap for Valian's internal developer tools. Each tool here is a
small, focused utility — typically a shell script — that solves a friction
point in our daily workflows.

## Layout

```
homebrew-tools/
├── bin/                 # The actual scripts/binaries shipped by each formula
│   └── frn              # Fast `flutter run` launcher with device picker
├── Formula/             # One Ruby formula per tool
│   └── frn.rb
├── .github/workflows/   # CI (shellcheck, formula audit)
└── README.md
```

Install any tool with `brew install valian-ca/tools/<name>` once the tap is added
(`brew tap valian-ca/tools` is implicit on first install).

## Conventions for adding a new tool

**Language.** Bash is the default for small tools — cold-start latency is
zero and portability across the team's macOS machines is trivial. Reach for
Go, Rust, or Dart only when bash is genuinely the wrong fit (heavy parsing,
real concurrency, binary protocols).

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

Pattern (see `Formula/frn.rb` for a concrete example):

```ruby
class Frn < Formula
  desc "Fast Flutter run launcher with device picker"
  homepage "https://github.com/valian-ca/homebrew-tools"
  url "https://github.com/valian-ca/homebrew-tools/archive/refs/tags/frn-0.1.0.tar.gz"
  sha256 "..."
  license "MIT"  # or whatever we agree on

  depends_on "gum"
  depends_on "jq"

  def install
    bin.install "bin/frn"
  end

  test do
    assert_match "frn", shell_output("#{bin}/frn --help")
  end
end
```

The formula name (filename + class) matches the binary name. Use
per-tool git tags like `frn-0.1.0` so multiple tools can version
independently.

## Release process

1. Make changes on a branch, PR, review, merge to `main`.
2. CI runs `shellcheck` on `bin/*` and `brew audit --strict` on any changed
   formula.
3. Tag the release: `git tag frn-0.1.1 && git push --tags`.
4. Create a GitHub release for the tag (optional but tidy — the tag alone is
   enough for brew to resolve the tarball URL).
5. Update the formula's `url`, `version` (implicit from the tag), and
   `sha256` in a separate commit. The new sha256 comes from
   `curl -sL <tarball-url> | shasum -a 256`.

Teammates pick up the update with `brew upgrade valian-ca/tools/<name>`.

## Nerd Font glyphs

Some scripts (`frn` at least) use Nerd Font glyphs from the Private Use Area
for device/platform icons. Editing tools occasionally strip these on write.
If a script's icons vanish, verify with:

```
grep -n 'printf\|jq -r' bin/<script> | xxd | grep -i 'ef85'
```

Bytes `ef 85 bb` and `ef 85 b9` correspond to  (android) and  (apple)
respectively. If they're gone, re-insert via a binary-safe write (Python,
`printf '\xee\x85\xbb'`, etc.) — not via every editor's UTF-8 "helpful"
normalisation.

## CI

`.github/workflows/lint.yml` runs:
- `shellcheck -x bin/*` on every PR and push to `main`
- `brew audit --strict --online Formula/*.rb` if any formula changed

Both are fast (<30s). Keep them mandatory via branch protection on `main`.

## When working with Claude Code in this repo

This tap is co-owned by the team. When asked to add or modify a tool:

- Read the existing tool(s) first to match conventions (shebang, flag
  parsing, help, error handling, set -euo pipefail).
- Don't introduce runtime dependencies without adding `depends_on` in the
  formula and updating this file's conventions list if it's a new kind of
  dep.
- Always update `bin/<tool>` and `Formula/<tool>.rb` in the same PR.
- Bump the version in the formula when the script changes — even trivially.
  Team members shouldn't have to think about whether `brew upgrade` is a
  no-op.
- Preserve PUA glyphs byte-for-byte if you touch a script that uses them.
