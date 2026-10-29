# lanes — the contract

`lanes` gives each git worktree of a project its own local stack: a block of
ports, a short `TMPDIR`, the project's resources and its processes, run by
[process-compose](https://github.com/F1bonacc1/process-compose) under the
`lanesd` daemon. It is a tool for agents working in worktrees: nobody is asked
to use it, and a project that adopts it keeps working on its main checkout
without it.

This page is the contract a project follows to adopt lanes. The demo project
in [`examples/lanes-demo`](../../examples/lanes-demo) follows it.

## Install

```sh
brew install valian-ca/tools/lanes
brew services start lanes
```

`lanes` and `lanesd` run on macOS only. process-compose comes as a dependency,
from its author's tap (`f1bonacc1/tap`); if your Homebrew refuses to tap it on
its own, run `brew tap f1bonacc1/tap` first. lanes refuses a process-compose
older than 1.73.0 and names both versions.

## Concepts

- A **worktree** is a linked checkout made by `claude --worktree`, by Claude
  Desktop or by `git worktree add`. Its port block and its `TMPDIR` belong to
  it for its whole life.
- A **lane** is the data of one line of work in a worktree: everything its
  adapters create. A worktree carries one lane at a time, and may carry
  several one after the other.
- The **main checkout** is the repository's original checkout. It has no
  lane, keeps the project's fixed ports and works without lanes.
- A **session** is a process whose name is in `sessions` (`claude` by
  default) and whose current directory is in the worktree.
- An **adapter** creates, erases and lists one kind of resource, and says
  which lane each one belongs to.
- **Reconcile** is the pass of `lanesd` that clears what remains of a
  worktree that disappeared.

## The main checkout

lanes serves linked worktrees only. On the main checkout:

- `lanes start`, `up`, `restart`, `stop` and `logs` refuse, and point to the
  project's own commands;
- `lanes env` prints nothing;
- the hooks return at once without doing anything;
- `lanes list` never shows it.

In a repository without `lanes.yaml`, `lanes env` and the hooks do nothing,
and `lanes list` still lists every lane on the Mac; every other command
refuses and names the missing file.

## Versioned files

A project declares its stack in two files it writes and versions:

- `lanes.yaml`, below;
- `process-compose.yaml`, the project's own processes.

lanes never modifies a file git tracks. What it writes in the worktree —
`.lanes/` and the generated files — must be ignored by git; lanes refuses to
write a generated file that git tracks.

## `lanes.yaml`, schema 1

```yaml
schema: 1                     # required
ports:
  block: 20                   # required: ports reserved for the worktree, 1 to 1000
  names: [web, admin]         # PORT_WEB = block start, PORT_ADMIN = block start + 1
resources:                    # adapters, in creation order
  - scripts:
      name: demo-store        # letters, digits, '.', '_' or '-'
      create: tool/lane-store create
      erase: tool/lane-store erase
      list: tool/lane-store list
      owner: tool/lane-store owner
generate:
  - template: config/app.env.tmpl   # renders config/app.env
setup:
  - name: deps
    run: ./tool/install.sh
    when_changed: [pnpm-lock.yaml]   # absent: runs once per worktree
sessions: [claude]            # default [claude]
shared: [GradleDaemon]        # default []
spare: []                     # default []
ram_alert: 4GB                # default 4GB; KB, MB or GB
```

An unknown key is an error that names its path. A `schema` newer than the
installed lanes is refused with `brew upgrade valian-ca/tools/lanes`.

| Key | Meaning |
| --- | --- |
| `ports.block` | Size of the worktree's block. Blocks come from one table shared by every project on the Mac, between 20000 and 48999, below macOS's ephemeral ports. A block is given only when no other worktree holds it and none of its ports is open. |
| `ports.names` | Named ports, in block order. Each becomes `PORT_<NAME>`. |
| `resources` | The adapters. The core knows `scripts`; Firebase, MySQL and Postgres adapters come as separate releases. |
| `generate` | Templates rendered with Go's `text/template` and the lane's variables (`{{.PORT_WEB}}`). The output is the template's path without `.tmpl`; it must be ignored by git, and is rewritten only when its content changes. |
| `setup` | Prep steps, run in order with `/bin/sh -c` from the worktree root and the lane's variables. A step reruns only when one of its `when_changed` files changed since it last succeeded. |
| `sessions` | Process names that count as a session. |
| `shared` | Processes a teardown never stops, such as daemons shared across worktrees. |
| `spare` | Names added to the built-in list the sweep spares. |
| `ram_alert` | Above this total, `lanes list` flags the lane. It never stops it. |

A process matches `shared` or `spare` when its kernel name or the basename of
its `argv[0]` equals the entry, or when one of its arguments equals it or ends
with `/<entry>` or `.<entry>` — a JVM daemon's main class, for instance.

## Environment

`lanes env` and the hooks export:

| Variable | Value |
| --- | --- |
| `LANE_ID` | The lane: the slug of its branch at birth, truncated to 24 characters, then `_` and 6 random hex characters (`feature_val_369_noyau_3f9a1c`). |
| `LANES_WORKTREE` | The worktree's root. |
| `TMPDIR` | `/tmp/lanes-<uid>-<8 hex>`, 40 characters at most, the same for the worktree's whole life. It wins over a `TMPDIR` Claude Code sets. |
| `PORT_<NAME>` | Each named port. |
| `LANES_PORT_BASE`, `LANES_PORT_SIZE` | The block. |
| `LANES_VARS` | The names above, so `lanes env --unset` removes them all. |

The stack's processes, the prep steps, the adapters' commands and the
session's shells all get these variables. A project CLI that targets a remote
environment (`--env production`) applies `lanes env --unset` before loading
its configuration.

## Hooks and `.envrc`

The project's `.claude/settings.json` wires three hooks. Each is guarded, so a
Mac without lanes stays silent:

```json
{
  "hooks": {
    "SessionStart": [{ "hooks": [{ "type": "command", "command": "command -v lanes >/dev/null 2>&1 && lanes hook session-start || true" }] }],
    "CwdChanged": [{ "hooks": [{ "type": "command", "command": "command -v lanes >/dev/null 2>&1 && lanes hook cwd-changed || true" }] }],
    "SessionEnd": [{ "hooks": [{ "type": "command", "command": "command -v lanes >/dev/null 2>&1 && lanes hook session-end || true", "timeout": 5 }] }]
  }
}
```

- `session-start` and `cwd-changed` work on the directory the payload names,
  which is the worktree even when Desktop runs the hook from the main
  checkout. They settle the lane, run the prep steps, render the templates,
  and append the lane's exports to `$CLAUDE_ENV_FILE`. A first prep may be
  long: set the hook's `timeout` in the project's settings to fit it.
- `session-end` only tells `lanesd` that a session ended, and returns in well
  under a second.
- Hooks never block a session: they exit 0 and write problems to stderr.

No `WorktreeRemove` hook is needed: `lanesd` notices a deleted worktree on its
own.

The project's `.envrc`, for direnv:

```sh
if has lanes; then eval "$(lanes env)"; fi
```

## Lane identity

At every `lanes` command, hooks and `env` included, the core compares the
current branch with the lane recorded for the worktree.

- **Same lane**: the same branch; a detached `HEAD` (Desktop archiving, a
  rebase, a bisect); a branch renamed from the recorded one (`git branch -m`,
  followed through the reflog).
- **New lane**: the first pass; a branch with no link to the recorded one
  (Desktop reusing a worktree, `git switch`); the same branch moved back onto
  the default branch after its work merged — its tip left the recorded
  history, it holds no commit of its own any more, and it did before. A rebase
  keeps its own commits, so it keeps the lane.

When a new lane replaces one, before anything starts: the old stack stops,
every resource the old lane registered is erased by the adapter that created
it — from the worktree, or from the main checkout when the new branch lacks
the adapter's scripts — then the new lane is recorded. The port block and `TMPDIR` do not change.

## Prep

The hooks `session-start` and `cwd-changed` and `lanes start` prepare the
worktree, under a lock. `lanes env`, `list`, `stop` and `logs` never do. Two
built-in steps run first:

1. Copy from the main checkout each file `.worktreeinclude` names that the
   worktree lacks. One shell glob per line, relative to the root; `#` starts a
   comment and a `!` line excludes what it matches, wherever it stands in the
   file. Paths leaving the tree and symlinks are refused.
2. For each git-tracked `.env*.template`, create the file without
   `.template` when it is missing.

The declared `setup` steps follow. No step overwrites a file that exists.

## Adapters

An adapter implements:

| Operation | Contract |
| --- | --- |
| create | Make the lane's resources and return their names. Runs once per lane, at the lane's first `lanes start`. Must keep what already exists. |
| erase | Remove one resource of a lane. |
| list | Every resource of this adapter on the machine, whichever lane owns it. |
| owner | The lane a resource belongs to, or nothing when it is not a lane resource. |

It may also contribute processes. They go in `.lanes/adapters.yaml`, a
generated fragment process-compose merges *before* the project's
`process-compose.yaml`: the project can complete an adapter's process — its
dependencies, its variables — without declaring it again.

A resource living outside the worktree is written to the repository's
registry (`<git common dir>/lanes/registry.json`) as soon as `create` returns
it. Reconcile erases it once its worktree is gone, then asks each adapter of
the main checkout's `lanes.yaml` for `list` and `owner`, to catch a resource
created but never registered; the live lanes of every repository on the Mac
are kept. When an erase fails, the worktree keeps its port block and
reconcile tries again 5 minutes later.

### The `scripts` adapter

Four shell commands, run with `/bin/sh -c` from the worktree root, with a
2-minute timeout each. During reconcile the worktree is gone: they run from
the main checkout, with `lanesd`'s environment (Homebrew's `PATH`).

| Command | Gets | Prints |
| --- | --- | --- |
| `create` | the lane's variables, `LANES_LANE`, `LANES_WORKTREE` | one resource name per line |
| `erase` | `LANES_LANE`, `LANES_WORKTREE`, `LANES_RESOURCE` | nothing |
| `list` | `LANES_WORKTREE` | one resource name per line |
| `owner` | `LANES_RESOURCE` | the owning lane id, or nothing |

A non-zero exit fails the operation, and its stderr is reported.

## The stack

- `lanes start` is the only way a stack starts: nothing restarts on its own.
  It returns once every enabled piece runs and passes its readiness probe, or
  has completed with exit 0. A piece that errors, exits non-zero or restarts
  fails the start at once, with its name and the end of its log; a piece
  killed for lack of memory is reported as such. Each `lanes start`, `up`
  and `restart` has 20 minutes.
- There is one stack per worktree, shared by its sessions. `lanes start` on a
  running stack restarts nothing.
- `lanesd` looks for sessions every 10 s. After `SessionEnd`, it stops the
  stack as soon as no session is left. When sessions vanish without
  `SessionEnd` — a killed session — it waits 5 minutes for one to come back.
  A live session keeps its stack, however idle; only `lanes stop` stops it
  then.
- A teardown shuts process-compose down — one stop signal per process, which
  `shutdown.parent_only` aims at a group's leader — then stops every process
  still working in the worktree, by `SIGTERM` then `SIGKILL` after 10 s. It
  spares sessions, `shared`, `spare`, lanes itself, shells
  (`sh bash zsh fish dash nu`), editors (`vim nvim nano emacs code Code cursor
  Cursor Electron zed hx`) and terminals or multiplexers (`tmux screen zellij
  herdr login`). It also spares what a session or an editor launches — an MCP
  server, a language server, VS Code's extension host — down to the first
  shell: a dev server started from a shell, by an agent's Bash tool or by hand
  in a terminal, is stopped. What a running `lanes` command launches, a prep
  step or an adapter script, is spared too. It never touches a process working
  outside the worktree. The lane's data is kept.
- Each piece logs to `$TMPDIR/logs/<piece>.log`, readable after the stack
  stops. lanes sets each piece's `log_location`; a project must not.
- When `lanesd` restarts, it takes back the running stacks without restarting
  them. At login after a reboot, it reconciles and starts no stack.

## Commands

| Command | Does |
| --- | --- |
| `lanes start` | Settle the lane, prep, create resources on the lane's first start, start the stack and wait until it is ready. |
| `lanes stop [worktree]` | Stop a stack, the current worktree's by default, and keep its data. |
| `lanes list [--json]` | Every lane on the Mac, stopped ones included. |
| `lanes env [--unset]` | The lane's exports, or the unset line for them. |
| `lanes up <process>` | Start a process `process-compose.yaml` declares `disabled: true`. |
| `lanes restart <process>` | Restart one piece; the others keep running. |
| `lanes logs <piece> [-f] [-n N]` | The end of a piece's log, followed with `-f`. |
| `lanes hook session-start\|cwd-changed\|session-end` | The hooks' entry points. |

Every command has `--help`.

## Where state lives

| What | Where |
| --- | --- |
| Port table, daemon socket and state | `~/.local/state/lanes/` |
| Lane record, prep stamps | `<worktree's git dir>/lanes/`, gone with the worktree |
| Registry of out-of-worktree resources | `<git common dir>/lanes/registry.json` |
| Logs, process-compose socket | the lane's `TMPDIR` |
| Generated fragment | `<worktree>/.lanes/`, which ignores itself |
| `lanesd` log | `$(brew --prefix)/var/log/lanes.log` |
