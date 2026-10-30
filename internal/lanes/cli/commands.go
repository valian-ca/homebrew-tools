package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/valian-ca/homebrew-tools/internal/lanes/client"
	"github.com/valian-ca/homebrew-tools/internal/lanes/daemon"
	"github.com/valian-ca/homebrew-tools/internal/lanes/env"
	"github.com/valian-ca/homebrew-tools/internal/lanes/gitx"
	"github.com/valian-ca/homebrew-tools/internal/lanes/list"
	"github.com/valian-ca/homebrew-tools/internal/lanes/paths"
	"github.com/valian-ca/homebrew-tools/internal/lanes/ports"
	"github.com/valian-ca/homebrew-tools/internal/lanes/procinfo"
	"github.com/valian-ca/homebrew-tools/internal/lanes/proto"
	"github.com/valian-ca/homebrew-tools/internal/lanes/tmpdir"
	"github.com/valian-ca/homebrew-tools/internal/lanes/workspace"
)

func newStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start this worktree's stack and wait until it is ready",
		Long: `Start the stack of the worktree around the current directory, under lanesd,
and return once every piece is ready.

On the lane's first start, the project's adapters create its resources. With
a mysql resource, every start wakes the project's MySQL server when nothing
listens on its port — running the resource's start command from the main
checkout — and creates the lane's databases that are missing, empty. Prep
steps whose trigger changed run first, and templates are rendered. A stack
that already runs is left untouched.

The command fails, with a non-zero status, when lanesd is not running, when a
port of the block is held by another process, when a piece dies — then it
prints the piece's name and the end of its log — when the firebase resource
lacks a named port, when firebase-tools is missing from node_modules, or when
its socket path under TMPDIR is too long. It also fails when a process the
mysql resource's listeners do not name holds the server's port, when the
server sleeps and no start command is declared, when the start command fails
or the server does not accept connections 2 minutes after it, and when an
env: entry of lanes.yaml does not render. A start may take up to 20 minutes.

With a firebase resource, LANES_FIREBASE_JVM_HEAP (768MB) and
LANES_FIREBASE_FUNCTIONS_TIMEOUT (300, or 5m), when set, replace the
project's jvm_heap and functions_timeout.

When no Claude session works in the worktree, the stack still starts, and
stops after 5 minutes unless a session arrives.`,
		Example: `  lanes start
  lanes start && lanes list`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			dir, err := cwd()
			if err != nil {
				return err
			}
			if _, _, err := workspace.Locate(ctx, dir); err != nil {
				return err
			}
			binary, err := daemonReady(ctx)
			if err != nil {
				return err
			}
			w, err := settle(ctx, dir, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if w.EnvErr != nil {
				return w.EnvErr
			}
			if err := prepare(ctx, w, cmd.ErrOrStderr()); err != nil {
				return err
			}
			environ := w.Environ(os.Environ())
			for _, a := range w.Adapters(environ) {
				if err := a.Preflight(ctx); err != nil {
					return err
				}
			}
			if err := w.CreateResources(ctx, environ); err != nil {
				return err
			}
			callCtx, cancel := withTimeout(ctx, daemon.DefaultStartTimeout+2*time.Minute)
			defer cancel()
			resp, err := client.Call(callCtx, proto.Request{Op: proto.OpStart, Worktree: w.WT.Root, Environ: os.Environ(), Binary: binary})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if resp.AlreadyRunning {
				printf(out, "the stack of %s is already running — nothing restarted\n", w.WT.Root)
			} else {
				printf(out, "the stack of %s is ready\n", w.WT.Root)
			}
			printf(out, "lane %s, ports %d-%d\n", w.Lane.ID, w.Block.Start, w.Block.End())
			for i, n := range w.Cfg.Ports.Names {
				printf(out, "  %s=%d\n", env.PortVar(n), w.Block.Start+i)
			}
			printf(out, "logs in %s\n", paths.Logs(w.Tmpdir))
			if resp.NoSession {
				printf(out, "no Claude session in this worktree — the stack stops after 5 min without one\n")
			}
			return nil
		},
	}
}

func newStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop [worktree]",
		Short: "Stop a worktree's stack and keep its data",
		Long: `Stop the stack of a worktree — the current one by default — and keep its
data for the next lanes start. Processes started by hand from the worktree
stop too; shells, editors, terminals and the project's shared processes are
spared.`,
		Example: `  lanes stop
  lanes stop ~/code/acme/.claude/worktrees/fix-login`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := cwd()
			if err != nil {
				return err
			}
			if len(args) == 1 {
				if dir, err = filepath.Abs(args[0]); err != nil {
					return err
				}
			}
			return stop(cmd, dir)
		},
	}
}

// stop settles no lane and allocates nothing: stopping keeps the lane's data,
// and a stack started before its branch lost lanes.yaml must stay stoppable.
func stop(cmd *cobra.Command, dir string) error {
	ctx := cmd.Context()
	wt, _, err := workspace.Locate(ctx, dir)
	if errors.Is(err, gitx.ErrNotRepo) || errors.Is(err, workspace.ErrMainCheckout) {
		return err
	}
	_, hasBlock, ferr := ports.Find(wt.AdminDir)
	if ferr != nil {
		return ferr
	}
	if !hasBlock {
		if err != nil {
			return err
		}
		printf(cmd.OutOrStdout(), "no stack has run in %s — nothing to stop\n", wt.Root)
		return nil
	}
	if err := workspace.StopStack(ctx, wt.Root, tmpdir.Path(wt.CommonDir, wt.AdminDir)); err != nil {
		return err
	}
	printf(cmd.OutOrStdout(), "stopped the stack of %s — its data is kept\n", wt.Root)
	return nil
}

func newListCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List the lanes of every repository on this Mac",
		Long: `List every lane on this Mac, across repositories, stopped ones included:
worktree, lane, port block, state, session state, RAM per piece, last
activity and log directory. A lane above its RAM alert threshold is flagged,
never stopped. Main checkouts never appear. With --json, each lane also lists
its resources: what its current lane registered, MySQL databases included.`,
		Example: `  lanes list
  lanes list --json | jq '.lanes[] | select(.ramAlert)'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if dir, err := cwd(); err == nil {
				if _, _, err := workspace.Locate(ctx, dir); err != nil && !workspace.IsQuiet(err) {
					return err
				}
			}
			callCtx, cancel := withTimeout(ctx, 5*time.Second)
			defer cancel()
			var stacks []proto.StackStatus
			if resp, err := client.Call(callCtx, proto.Request{Op: proto.OpList}); err == nil {
				stacks = resp.Stacks
				if stacks == nil {
					stacks = []proto.StackStatus{}
				}
			}
			r, err := list.Build(ctx, stacks, procinfo.System())
			if err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(r)
			}
			list.WriteText(cmd.OutOrStdout(), r)
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print the inventory as JSON")
	return c
}

func newEnvCmd() *cobra.Command {
	var unset bool
	c := &cobra.Command{
		Use:   "env",
		Short: "Print this worktree's lane variables as shell exports",
		Long: `Print the lane's variables as export lines, for eval in a shell or .envrc:
LANE_ID, LANES_WORKTREE, TMPDIR, PORT_<NAME> for each named port,
LANES_PORT_BASE, LANES_PORT_SIZE, LANES_HOST and LANES_VARS. With a firebase
resource, also the SDK variable of each emulator that starts, on 127.0.0.1
(FIRESTORE_EMULATOR_HOST, FIREBASE_AUTH_EMULATOR_HOST…), FIREBASE_EMULATOR_HUB
and LANES_FIREBASE_CONFIG. With a mysql resource, LANES_MYSQL_HOST,
LANES_MYSQL_PORT, LANES_MYSQL_USER, LANES_MYSQL_PASSWORD and
LANES_MYSQL_DATABASE_<KEY> for each of the lane's databases, without touching
the server. Then the project's env: variables, in order. With --unset, print
the unset line that removes every variable lanes exports.

On a main checkout, outside a repository, or without lanes.yaml, it prints
nothing. It never runs a prep step, and never wakes a MySQL server. It fails
when an env: entry does not render.`,
		Example: `  eval "$(lanes env)"
  eval "$(lanes env --unset)"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			dir, err := cwd()
			if err != nil {
				return err
			}
			w, err := settle(ctx, dir, cmd.ErrOrStderr())
			if workspace.IsQuiet(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if unset {
				printf(cmd.OutOrStdout(), "%s", env.Unsets(w.Vars))
				return nil
			}
			if w.EnvErr != nil {
				return w.EnvErr
			}
			printf(cmd.OutOrStdout(), "%s", w.Vars.Exports())
			return nil
		},
	}
	c.Flags().BoolVar(&unset, "unset", false, "print the unset line for every variable lanes exports")
	return c
}

func newUpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "up <process>",
		Short: "Start an on-demand process of the running stack",
		Long: `Start a process the project declares disabled in process-compose.yaml — a
dev server, Storybook — and wait until it is ready. It listens on its named
port of the block.`,
		Example: `  lanes up storybook`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProcessOp(cmd, proto.OpUp, args[0], "%s is running\n")
		},
	}
}

func newRestartCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "restart <process>",
		Short:   "Restart one piece of the running stack",
		Long:    `Restart one piece of the running stack and wait until it is ready. The other pieces keep running untouched.`,
		Example: `  lanes restart api`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProcessOp(cmd, proto.OpRestart, args[0], "%s restarted\n")
		},
	}
}

func runProcessOp(cmd *cobra.Command, op, proc, done string) error {
	ctx := cmd.Context()
	dir, err := cwd()
	if err != nil {
		return err
	}
	if _, _, err := workspace.Locate(ctx, dir); err != nil {
		return err
	}
	if _, err := daemonReady(ctx); err != nil {
		return err
	}
	w, err := settle(ctx, dir, cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	callCtx, cancel := withTimeout(ctx, daemon.DefaultStartTimeout+time.Minute)
	defer cancel()
	if _, err := client.Call(callCtx, proto.Request{Op: op, Worktree: w.WT.Root, Proc: proc}); err != nil {
		return err
	}
	printf(cmd.OutOrStdout(), done, proc)
	return nil
}

func newLogsCmd() *cobra.Command {
	var (
		follow bool
		lines  int
	)
	c := &cobra.Command{
		Use:   "logs <piece>",
		Short: "Print the end of a piece's log, and follow it with -f",
		Long: `Print the last lines of a piece's log. Logs live under the lane's TMPDIR and
stay readable after the stack stops; they go away with the worktree.`,
		Example: `  lanes logs web
  lanes logs web -f
  lanes logs api -n 200`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			dir, err := cwd()
			if err != nil {
				return err
			}
			w, err := settle(ctx, dir, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			return showLog(ctx, cmd.OutOrStdout(), w.Tmpdir, args[0], lines, follow)
		},
	}
	c.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new lines as they arrive")
	c.Flags().IntVarP(&lines, "lines", "n", 40, "number of lines to print")
	return c
}

func errorf(format string, a ...any) error { return fmt.Errorf(format, a...) }
