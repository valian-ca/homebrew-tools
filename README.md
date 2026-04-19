# homebrew-tools

A Homebrew tap for Valian's internal developer tools.

## Install the tap

```sh
brew tap valian-ca/tools
```

The `homebrew-` prefix is dropped from the tap name by Homebrew convention.

## Available tools

| Tool  | Description                                   | Install                                |
| ----- | --------------------------------------------- | -------------------------------------- |
| `frn` | Fast `flutter run` launcher with device picker | `brew install valian-ca/tools/frn`    |

## Upgrading

```sh
brew update
brew upgrade valian-ca/tools/<name>
```

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

- `shellcheck -x bin/*` — lints all shell scripts.
- `brew audit --strict --online Formula/*.rb` — only when a formula changes.
