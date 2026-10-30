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
brew install f1bonacc1/tap/process-compose valian-ca/tools/lanes
brew services start lanes
```

`lanes` and `lanesd` run on macOS only. process-compose comes from its
author's tap (`f1bonacc1/tap`). Homebrew taps a third-party tap only for a
formula named in full on the command line, never for a dependency, so the
install names process-compose too. lanes refuses a process-compose older
than 1.73.0 and names both versions.

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

- `lanes start`, `up`, `restart`, `stop`, `logs` and `host` refuse, and point
  to the project's own commands;
- `lanes test-env -- <command>` runs the command as is;
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
`.lanes/`, the generated files, and a `firebase` resource's generated configs
and data — must be ignored by git; lanes refuses to write a generated file that
git tracks, and warns when one is not ignored. It never edits `.gitignore` or
`.git/info/exclude`.

## `lanes.yaml`, schema 2

```yaml
schema: 2                     # required: 1 or 2
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
  - firebase:                 # at most one per project
      config: firebase.emulator.json
      seed: seed
  - mysql:                    # at most one per project; schema 2
      start: docker compose -f pilote/docker-compose.yaml up -d --wait
      listeners: [com.docker.backend]
      databases:
        dev: nezrouge_{lane}
        test: nezrouge_test_{lane}
env:                          # schema 2; in order, each sees the ones before; a name once
  PILOTE_DATABASE_URL: jdbc:mysql://{{.LANES_MYSQL_HOST}}:{{.LANES_MYSQL_PORT}}/{{.LANES_MYSQL_DATABASE_DEV}}
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
installed lanes is refused with `brew upgrade valian-ca/tools/lanes`: lanes
0.2.0 refuses a schema-2 file that way. lanes 0.3.0 reads schemas 1 and 2. A
schema-1 file runs as it did under 0.2.0; in it, a `mysql` resource or an
`env` key is an error that names the key and `schema: 2`.

| Key | Meaning |
| --- | --- |
| `ports.block` | Size of the worktree's block. Blocks come from one table shared by every project on the Mac, between 20000 and 48999, below macOS's ephemeral ports. A block is given only when no other worktree holds it and none of its ports is open. |
| `ports.names` | Named ports, in block order. Each becomes `PORT_<NAME>`. |
| `resources` | The adapters, one per entry: `scripts`, `firebase` and `mysql`. A Postgres adapter comes as a separate release. |
| `env` | The project's own variables, see [below](#env). |
| `generate` | Templates rendered with Go's `text/template` and the lane's variables (`{{.PORT_WEB}}`). The output is the template's path without `.tmpl`; it must be ignored by git, and is rewritten only when its content changes. |
| `setup` | Prep steps, run in order with `/bin/sh -c` from the worktree root and the lane's variables. A step reruns only when one of its `when_changed` files changed since it last succeeded. |
| `sessions` | Process names that count as a session. |
| `shared` | Processes a teardown never stops, such as daemons shared across worktrees. |
| `spare` | Names added to the built-in list the sweep spares. |
| `ram_alert` | Above this total, `lanes list` flags the lane. It never stops it. |

A process matches `shared` or `spare` when its kernel name or the basename of
its `argv[0]` equals the entry, or when one of its arguments equals it or ends
with `/<entry>` or `.<entry>` — a JVM daemon's main class, for instance.

### `env`

`env` declares the project's variables, in order, each a Go `text/template`
like `generate`'s. A template sees every lane variable, adapters' included,
and the `env` entries before it. A variable it names but the lane lacks is an
error.

- `lanes env` and the hooks export them, the stack's processes, the prep
  steps, the `generate` templates and `lanes test-env` get them, and they are
  in `LANES_VARS`, so `lanes env --unset` removes them. Under `lanes
  test-env`, they are rendered again over the test series' values.
- An entry that redefines a variable lanes sets — `TMPDIR`, `LANE_ID`,
  `PORT_*`, `LANES_*`, a Firebase emulator variable — is an error of
  `lanes.yaml` that names the entry's path (`env.PORT_WEB`).
- A template that fails to render fails `lanes start` and `lanes env`, naming
  the entry; a hook writes the error to stderr and exports everything else.
- It serves any project, with or without `mysql`. On the main checkout,
  nothing is exported.

## Environment

`lanes env` and the hooks export:

| Variable | Value |
| --- | --- |
| `LANE_ID` | The lane: the slug of its branch at birth, truncated to 24 characters, then `_` and 6 random hex characters (`feature_val_369_noyau_3f9a1c`). |
| `LANES_WORKTREE` | The worktree's root. |
| `TMPDIR` | `/tmp/lanes-<uid>-<8 hex>`, 40 characters at most, the same for the worktree's whole life. It wins over a `TMPDIR` Claude Code sets. |
| `PORT_<NAME>` | Each named port. |
| `LANES_PORT_BASE`, `LANES_PORT_SIZE` | The block. |
| `LANES_HOST` | The host the lane's clients reach its stack by: `127.0.0.1`, or the Mac's network IP after `lanes host lan`. |
| Firebase variables | With a `firebase` resource, see [below](#the-firebase-adapter). |
| MySQL variables | With a `mysql` resource, see [below](#the-mysql-adapter). |
| The project's `env` | See [`env`](#env). |
| `LANES_VARS` | The names above, so `lanes env --unset` removes them all. |

The stack's processes, the prep steps, the adapters' commands and the
session's shells all get these variables — except a `mysql` resource's
`start`, which runs without them (see [below](#the-mysql-adapter)). Prep steps
run before `lanes start` wakes the MySQL server and makes the lane's
databases: they get the `LANES_MYSQL_*` variables but must not rely on the
database, which a lane's first start has not made yet. A project CLI that targets a remote
environment (`--env production`) applies `lanes env --unset` before loading
its configuration.

## Hooks and `.envrc`

The project's `.claude/settings.json` wires four hooks. Each is guarded, so a
Mac without lanes stays silent:

```json
{
  "hooks": {
    "SessionStart": [{ "hooks": [{ "type": "command", "command": "command -v lanes >/dev/null 2>&1 && lanes hook session-start || true" }] }],
    "CwdChanged": [{ "hooks": [{ "type": "command", "command": "command -v lanes >/dev/null 2>&1 && lanes hook cwd-changed || true" }] }],
    "PostToolUse": [{ "matcher": "EnterWorktree|ExitWorktree", "hooks": [{ "type": "command", "command": "command -v lanes >/dev/null 2>&1 && lanes hook cwd-changed || true" }] }],
    "SessionEnd": [{ "hooks": [{ "type": "command", "command": "command -v lanes >/dev/null 2>&1 && lanes hook session-end || true", "timeout": 5 }] }]
  }
}
```

- `session-start` and `cwd-changed` work on the directory the payload names,
  which is the worktree even when Desktop runs the hook from the main
  checkout. They settle the lane, run the prep steps, render the templates,
  and write the lane's exports to `$CLAUDE_ENV_FILE`. A first prep may be
  long: set the hook's `timeout` in the project's settings to fit it.
- Start a session in its worktree, with `claude --worktree` or from Desktop.
  `EnterWorktree` and `ExitWorktree` fire neither `CwdChanged` nor
  `SessionStart`, and Claude Code reads a session's env files again only
  after those hooks, or before its first Bash command. The `PostToolUse` hook
  covers the first `EnterWorktree` of a session opened on the main checkout:
  `cwd-changed` rewrites the env files the session's lanes hooks got. After
  an `ExitWorktree` or a second `EnterWorktree`, Bash commands keep the first
  lane's variables until the session moves with `cd`, which fires
  `CwdChanged`.
- `session-end` only tells `lanesd` that a session ended, and returns in well
  under a second. A session opened by `claude --worktree` has already left its
  worktree when the hook runs, so `session-end` also tells `lanesd` about every
  worktree where `session-start` or `cwd-changed` saw the payload's
  `session_id`.
- Hooks never block a session: they exit 0 and write problems to stderr.

No `WorktreeRemove` hook is needed: `lanesd` notices a deleted worktree on its
own, as git does, by the missing `gitdir` file in its admin directory, even
when the directory itself is left behind.

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

An erase whose resource cannot be reached — a `mysql` server that sleeps or
sits behind a foreign listener — does not hold the new lane back: the
resource stays registered, the command writes the error to stderr, and
`lanesd` retries every 5 minutes. The new lane's resources have new names.

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
| create | Make the lane's resources and return their names. Runs once per lane, at the lane's first `lanes start` — at every `lanes start` for `mysql`. Must keep what already exists. |
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
are kept, and a resource that may belong to several lanes is kept while any
of them lives. When an erase fails, the worktree keeps its port block and
reconcile tries again 5 minutes later. Reconcile also erases what a live
worktree's earlier lanes still have registered, at the same pace.

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

### The `mysql` adapter

It wakes the project's shared MySQL server and keeps each lane's databases
existing and empty. Filling them is the project's business (the seed task,
below). Schema 2.

| Key | Meaning |
| --- | --- |
| `port` | Default `3306`. The server runs on this Mac, and lanes always reaches it at `127.0.0.1`: there is no `host` key, since the port guard can only vouch for this Mac's listener. |
| `user` | Default `root`. |
| `password` | Default empty. |
| `start` | The command that wakes the server, such as `docker compose -f pilote/docker-compose.yaml up -d --wait`. Optional. |
| `listeners` | The process names that may hold `port`. Default `[mysqld]`. |
| `databases` | Required, at least one: a key of letters, digits and `_`, and a name template holding `{lane}` after a fixed prefix (`app_{lane}`, not `{lane}`). |

A template is refused when it can make a name over MySQL's 64 characters with
the longest lane ID, 31 characters, and so are two keys that differ only by
case, since they would share one variable. A second `mysql` resource is
refused too.

**The server is the main checkout's, and shared.** No lane stops it: not
`lanes stop`, not a teardown, a new lane or reconcile. lanes connects to it
over TCP by itself; no `mysql` client is needed on the Mac. The adapter adds
no process to the stack.

**Port guard.** Before any operation on the server — waking it, creating,
erasing, reconciling — lanes looks at which process listens on `port`. When
it is not one of `listeners`, lanes refuses without connecting, naming the
process and its PID. A Cloud SQL proxy or an SSH tunnel on the port speaks
MySQL and leads to production; the guard keeps lanes off it. It knows the
program holding the port, not the container: another Docker container
published on the same port would pass. A port that answers while lanes sees
no listener — a process of another user — is refused too. To find the name
to declare, run `lsof -nP -iTCP:<port> -sTCP:LISTEN` with the server up:

| Installation | `listeners` | Proven |
| --- | --- | --- |
| Homebrew `mysql@8.4` or `mysql`, MAMP, DBngin | `[mysqld]`, the default | Homebrew, in CI |
| Docker Desktop | `[com.docker.backend]` | by hand, before the 0.3.0 release |
| Official `.pkg` (runs as `_mysql`) | refused: lanes cannot see another user's listener | — |
| OrbStack, Colima, Podman, MariaDB | find it with `lsof` | no |
| Cloud SQL proxy, SSH tunnel | refused, by design | — |

**Waking.** At `lanes start`, when nothing listens on `port`, lanes runs
`start` with `/bin/sh -c`, from the main checkout — a server's data never
lives in a worktree that can disappear — with the caller's environment
without the lane's variables, under a lock of the Mac keyed on the port: two
lanes starting together — of one repository, of two clones, or of two
projects on the same server — never run it at once, and the second finds the
server up.
Then it waits up to 2 minutes for the server to accept connections. When
`start` fails or the wait runs out, `lanes start` refuses with the end of
`start`'s output and starts nothing. Without `start`, a sleeping server makes
`lanes start` refuse, naming `host:port`: start it with the project's own
commands. Only `lanes start` wakes the server — never a hook, `lanes env`, a
new lane, reconcile or `lanesd` at login.

**The lane's databases.** Each is its template with `{lane}` replaced by the
lane ID. At every `lanes start`, before the stack starts, the adapter makes
sure each exists: it creates the missing ones empty and registers each the
moment it exists. A database dropped by hand comes back empty at the next
`lanes start`; one that exists is never modified. Stopping the stack leaves
them alone.

**Erasing and ownership.** A new lane erases the old lane's registered
databases before anything starts; reconcile erases a gone worktree's. Erasing
one that no longer exists succeeds. When the server sleeps, does not answer
or the guard refuses, the erase fails without waking it: the databases stay
registered, reconcile retries every 5 minutes, and a new lane does not wait.
An erase connects with the user and password of the current `lanes.yaml` —
the worktree's, or the main checkout's once the worktree is gone — and falls
back to those recorded at registration when that file has no `mysql`
resource: a rotated password does not strand a retired lane's databases.
A database belongs to a lane only when its name comes out of one of the
project's templates with a valid lane ID — ending in `_` and 6 hex
characters. The main checkout's databases (`nezrouge`, `nezrouge_test`) and
those made by hand are never touched. Reconcile also erases a lane database
never registered whose lane is gone. A name can come out of two templates —
`nezrouge_test_foo_abc123` is the `dev` database of lane `test_foo_abc123`
and the `test` database of lane `foo_abc123` — and is then erased only when
none of those lanes lives.

**Variables.** `lanes env` and the hooks export `LANES_MYSQL_HOST` (always
`127.0.0.1`),
`LANES_MYSQL_PORT`, `LANES_MYSQL_USER`, `LANES_MYSQL_PASSWORD` (even empty) and
`LANES_MYSQL_DATABASE_<KEY>` for each database, its key upper-cased:
`LANES_MYSQL_DATABASE_DEV`, `LANES_MYSQL_DATABASE_TEST`. They are computed
without the server, so a sleeping server does not stop them. They are in
`LANES_VARS`; the stack's processes, the prep steps and the `generate`
templates get them; `lanes test-env` leaves them unchanged.

**The seed task.** lanes never fills a database. The project seeds in a task
of its `process-compose.yaml`, which the stack runs once the lane's databases
exist: a new lane, or a database dropped by hand, reaches it empty. A first
`lanes start` that fails can leave a database half filled, and lanes never
erases one because a start failed, so the task must tell a half-filled
database from a filled one. MySQL commits each `CREATE TABLE` at once, so a
transaction alone cannot: keep a marker table, created last.

```yaml
processes:
  mysql-seed:
    command: tool/mysql-seed
```

```sh
# without the marker, drop whatever tables a cut-short seed left, then seed
if [ "$(sql -e "SELECT COUNT(*) FROM information_schema.TABLES
              WHERE TABLE_SCHEMA = '$LANES_MYSQL_DATABASE_DEV' AND TABLE_NAME = 'lanes_seed'")" = 0 ]; then
  drop_every_table "$LANES_MYSQL_DATABASE_DEV"
  load_the_seed "$LANES_MYSQL_DATABASE_DEV"
  sql "$LANES_MYSQL_DATABASE_DEV" -e 'CREATE TABLE lanes_seed (seeded_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)'
fi
```

The demo's `tool/mysql-seed` follows this model. Pieces that need the seed
depend on the task with `condition: process_completed_successfully`.

### The `firebase` adapter

The Firebase emulators of the lane, common to every project. A project
declares at most one `firebase` resource; a second is an error of
`lanes.yaml` that names its path.

**Prerequisites.** The emulators need Java 21 or later on `PATH`, and Node 22
for firebase-tools and the functions. The project installs firebase-tools in
its dependencies: lanes runs the first `node_modules/.bin/firebase` from the
base config's directory up to the worktree, unless `command` names another.

```yaml
resources:
  - firebase:
      config: firebase.emulator.json  # required: the versioned base config
      data: .emu-data                 # default .emu-data: the lane's data, git-ignored
      seed: seed                      # optional: imported at a lane's first start
      only: [auth, firestore]         # default: every emulator of the base config
      test:
        only: [firestore]             # default: the stack's, UI excepted
      jvm_heap: 512MB                 # default 512MB
      functions_timeout: 180s         # default 180s
      export_timeout: 60s             # default 60s, 75s at most
      command: npx firebase           # optional: replaces the project's firebase-tools
```

**Ports.** Each emulator that starts needs a named port, under a reserved
name: `auth`, `firestore`, `firestore_ws`, `database`, `functions`, `pubsub`,
`storage`, `eventarc`, `tasks`, `hosting`, `ui`, `hub`, `logging`. The `test_`
prefix names the test series' (`test_firestore`, `test_hub`…). The required
ports are those of the emulators that start, plus those Firebase starts with
them: `hub` and `logging` always (firebase-tools 15 starts the logging
emulator in every session); `ui` when the stack runs an emulator the UI shows
(Auth, Database, Firestore, Functions, Storage) and `ui.enabled` is not false;
`eventarc` and `tasks` with Functions; `firestore_ws` with Firestore.
`only` and `test.only` name at least one emulator that starts on its own:
`eventarc`, `tasks` and `ui` alone are an error of `lanes.yaml`.
`lanes start` refuses when a stack port is missing, and `lanes test-env` when a
`test_*` port is, each naming every missing port.

**Generated configs.** From the base config, lanes writes
`firebase.lanes.json` for the stack and `firebase.lanes.test.json` for the test
series, beside the base config — firebase-tools resolves the functions source,
the rules and the hosting files from the config's directory. Each keeps the
base config, but for the emulators: only the series' remain, each on its port
of the block and listening on every interface (`0.0.0.0`); the top-level keys
of the products that do not start go too, so that `--config` alone starts
exactly the series. The test config disables the UI. The project git-ignores
both; they are rewritten only when their content changes, at the
`session-start` and `cwd-changed` hooks, at `lanes start` and at
`lanes test-env`. When the base config is missing or invalid, or a stack port
is missing, both are removed: a hook writes the error to stderr and still
exports the lane, `lanes start` refuses and names the cause. A test series
short of ports only loses its own config. lanes never writes the base config,
nor anything on the main checkout.

**The `firebase` process.** The fragment declares a `firebase` process; the
project completes it in its `process-compose.yaml` — `depends_on` a build,
`environment` — without declaring it again. lanes runs the first
`node_modules/.bin/firebase` from the base config's directory up to the
worktree root; without one, `lanes start` refuses and says to install the
project's dependencies. `command` replaces it: lanes runs
`/bin/sh -c '<command> "$@"'` with its arguments, from `.lanes/firebase`.
firebase-tools runs from `<worktree>/.lanes/firebase`, so its debug logs and an
export's temporary files never land in the worktree.

- **Readiness.** The piece is ready once firebase-tools prints
  `All emulators ready` — the import is done and every function is loaded by
  then — and every required port answers. A
  `Failed to load function definition` line fails the start at once. A
  project's own probe, such as a collection its seed must hold, stays the
  project's: a process that `depends_on` `firebase`.
- **Stop.** One `SIGTERM` to firebase-tools, which exports into
  `<data>.next`. Only an export that completes, with its
  `firebase-export-metadata.json`, replaces `<data>`. After `export_timeout`,
  the emulators are killed, `<data>` keeps the last complete export, and
  `lanes logs firebase` says the export was abandoned. process-compose gives
  the piece `export_timeout` + 15 s.
- **Functions workers.** Before each start of the piece (`lanes start`,
  `lanes restart firebase`) and at each stop, lanes ends what an earlier
  firebase piece left — firebase-tools, its emulators and its functions
  workers, once their parent is gone. A piece that outlived a crashed
  process-compose first gets the stop process-compose would have sent, and
  `export_timeout` + 15 s to export. The workers of a running
  `lanes test-env` keep their parent and are spared.
- **Sockets.** Before emulators start, for the stack and for `test-env`, lanes
  computes the longest socket path they open under `TMPDIR`
  (`fire_emu_<16 hex>.sock`, a functions worker's), and refuses it at 104
  bytes or more — macOS keeps 104 bytes for a socket path, its final NUL
  included —, naming the path, its length and the limit.

**Data.** At start, a complete export in `data` is imported; otherwise the
`seed`; without one, the lane starts empty. The stop always exports into
`data`; the seed is never written. When a new lane is born, the old lane's
`data` is erased before anything starts: it is a registered resource.

**Caps.** Each emulator JVM, of the stack and of `test-env`, gets
`JAVA_TOOL_OPTIONS=-Xmx<jvm_heap>`, before any inherited value. Functions get
`functions_timeout` to load (`FUNCTIONS_DISCOVERY_TIMEOUT`).
`LANES_FIREBASE_JVM_HEAP` (`768MB`) and `LANES_FIREBASE_FUNCTIONS_TIMEOUT`
(`300`, or `5m`), when set in the environment of `lanes start` or
`lanes test-env`, replace the project's values.

**Variables.** For each emulator that starts, `lanes env` exports the SDKs'
variable on `127.0.0.1` and its port: `FIRESTORE_EMULATOR_HOST`,
`FIREBASE_AUTH_EMULATOR_HOST`, `FIREBASE_STORAGE_EMULATOR_HOST`,
`FIREBASE_DATABASE_EMULATOR_HOST`, `PUBSUB_EMULATOR_HOST`,
`CLOUD_TASKS_EMULATOR_HOST`; plus `FIREBASE_EMULATOR_HUB` and
`LANES_FIREBASE_CONFIG`, the path of `firebase.lanes.json`. `GCLOUD_PROJECT` is
never exported. All of them are in `LANES_VARS`, so `lanes env --unset`
removes them, and `generate` templates see them.

### `lanes test-env -- <command>`

Runs a command in the foreground against the lane's test series. Every
firebase `PORT_<NAME>`, the SDK variables and `LANES_FIREBASE_CONFIG` point at
the `test_*` ports and `firebase.lanes.test.json`; `TMPDIR` is `$TMPDIR/test`;
the caps apply. The project's [`env`](#env) entries are rendered again over
these values; the MySQL variables stay the lane's. An `env` entry that does
not render is written to stderr and left out. `lanes test-env` never wakes a
MySQL server nor creates the lane's databases: run `lanes start` once first.
The command starts the emulators itself:

```sh
lanes test-env -- firebase emulators:exec --config "$LANES_FIREBASE_CONFIG" "npm test"
```

The test config lists only `test.only` and the emulators Firebase starts with
them, without the UI. lanes prepares the worktree as `lanes start` does, then
imports and exports nothing: the lane's data is never touched. It works
whether the stack runs or not, and never touches the stack; `lanes stop`
spares a running `test-env`. Before and after the command, lanes ends what a
`test-env` killed outright left listening on the `test_*` ports — the
emulators and the firebase-tools that ran them, once their parent is gone —
and the functions workers it orphaned. One runs at a time in a lane: a second refuses and
names the PID of the first. On a main checkout, outside a repository or
without `lanes.yaml`, the command runs as is. The exit status is the
command's.

### Lane host

A physical device reaches the lane's emulators by the Mac's IP, not by
`127.0.0.1`. `LANES_HOST` is `127.0.0.1` by default.

- `lanes host lan` switches it to the IP of the interface that carries the
  default route, read anew at each call. Without a network, it refuses and the
  host does not change. `lanes host local` brings back `127.0.0.1`.
  `lanes host` prints it.
- Switching renders the templates again and never restarts the stack. Shells
  already open keep the old value, which still works on the Mac.
- The host lasts as long as the lane: it survives stops and restarts of the
  stack, and goes back to `127.0.0.1` with a new lane. `lanes list` shows each
  lane's.
- It serves any project, with or without Firebase.
- The emulators listen on every interface even in `local` mode, so the local
  network reaches them either way.

## The stack

- `lanes start` is the only way a stack starts: nothing restarts on its own.
  It returns once every enabled piece runs and passes its readiness probe, or
  has completed with exit 0. A piece that errors, exits non-zero or restarts
  fails the start at once, with its name and the end of its log; a piece
  killed for lack of memory is reported as such. Each `lanes start`, `up`
  and `restart` has 20 minutes.
- There is one stack per worktree, shared by its sessions. `lanes start` on a
  running stack restarts nothing.
- Before it checks the block's ports, `lanes start` ends what a crashed stack
  left behind, listening or not: a process working in the worktree whose
  parent is gone, with what it started, under the teardown's rules below. A
  watcher or a queue consumer never runs beside its new copy. A port held
  by anything else — a process outside the worktree, one a live shell or
  session still owns — fails the start, naming the process and its PID.
- `lanesd` looks for sessions every 10 s. After `SessionEnd`, it stops the
  stack as soon as no session is left. When sessions vanish without
  `SessionEnd` — a killed session — it waits 5 minutes for one to come back.
  A live session keeps its stack, however idle; only `lanes stop` stops it
  then.
- A teardown shuts process-compose down — one stop signal per process, which
  `shutdown.parent_only` aims at a group's leader — and waits for it as long
  as the slowest `shutdown.timeout_seconds` of the stack plus 15 s, a minute
  at least: Firebase declares its `export_timeout` plus 15 s. Then it stops
  every process still working in the worktree, by `SIGTERM` then `SIGKILL`
  after 10 s. It
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
| `lanes start` | Settle the lane, prep, create resources on the lane's first start — wake the MySQL server and ensure the lane's databases at every start — start the stack and wait until it is ready. |
| `lanes stop [worktree]` | Stop a stack, the current worktree's by default, and keep its data. |
| `lanes list [--json]` | Every lane on the Mac, stopped ones included. With `--json`, each lane's `resources`: its current lane's registered resources, `{adapter, kind, resource}`. |
| `lanes env [--unset]` | The lane's exports, or the unset line for them. |
| `lanes up <process>` | Start a process `process-compose.yaml` declares `disabled: true`. |
| `lanes restart <process>` | Restart one piece; the others keep running. |
| `lanes logs <piece> [-f] [-n N]` | The end of a piece's log, followed with `-f`. |
| `lanes test-env -- <command>` | Run a command against the lane's test series of emulators. |
| `lanes host [lan\|local]` | Show or switch `LANES_HOST`. |
| `lanes hook session-start\|cwd-changed\|session-end` | The hooks' entry points. |

Every command has `--help`.

## Where state lives

| What | Where |
| --- | --- |
| Port table, daemon socket and state | `~/.local/state/lanes/` |
| Lane record, prep stamps | `<worktree's git dir>/lanes/`, gone with the worktree |
| Registry of out-of-worktree resources | `<git common dir>/lanes/registry.json` |
| Logs, process-compose socket | the lane's `TMPDIR` |
| Generated fragment, firebase-tools' working directory | `<worktree>/.lanes/`, which ignores itself |
| Generated firebase configs | beside the base config, git-ignored by the project |
| Firebase data | `data`, `.emu-data` by default, git-ignored by the project |
| `lanes test-env` lock and `TMPDIR` | `$TMPDIR/test` |
| `lanesd` log | `$(brew --prefix)/var/log/lanes.log` |
