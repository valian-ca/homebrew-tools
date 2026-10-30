package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/valian-ca/homebrew-tools/internal/lanes/client"
	"github.com/valian-ca/homebrew-tools/internal/lanes/firebase"
	"github.com/valian-ca/homebrew-tools/internal/lanes/generate"
	"github.com/valian-ca/homebrew-tools/internal/lanes/pc"
	"github.com/valian-ca/homebrew-tools/internal/lanes/prep"
	"github.com/valian-ca/homebrew-tools/internal/lanes/workspace"
)

func NewRoot(version, contract string) *cobra.Command {
	root := &cobra.Command{
		Use:   "lanes",
		Short: "Give each git worktree its own local stack",
		Long: `lanes gives each git worktree of a project its own local stack: a block of
ports, a short TMPDIR, the project's resources (databases, emulator data) and
its processes, run by process-compose under the lanesd daemon. The stack stops
when no Claude session works in the worktree any more; its data stays until
the worktree goes away.

lanes serves linked worktrees only. On a project's main checkout, run the
project's own commands.

The project declares its stack in lanes.yaml and process-compose.yaml, both
versioned. See the contract: lanes help contract`,
		Example: `  cd ~/code/acme/.claude/worktrees/fix-login
  lanes start              # start this worktree's stack, wait until it is ready
  eval "$(lanes env)"      # its ports and TMPDIR in this shell
  lanes list --json        # every lane on this Mac
  lanes help contract      # the contract a project follows to adopt lanes`,
		Version:       version,
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.AddCommand(
		newStartCmd(),
		newStopCmd(),
		newListCmd(),
		newEnvCmd(),
		newUpCmd(),
		newRestartCmd(),
		newLogsCmd(),
		newHookCmd(),
		newTestEnvCmd(),
		newHostCmd(),
		newFirebaseCmd(),
	)
	root.SetHelpCommand(newHelpCmd(contract))
	return root
}

func newHelpCmd(contract string) *cobra.Command {
	help := &cobra.Command{
		Use:   "help [command | contract]",
		Short: "Help about any command, or the contract: lanes help contract",
		Long: `Help about any command: lanes help <command> prints its usage.

lanes help contract prints, as markdown, the contract a project follows to
adopt lanes — the one of this installed version, without the network.`,
		Example: `  lanes help start
  lanes help contract`,
		Run: func(c *cobra.Command, args []string) {
			target, _, err := c.Root().Find(args)
			if target == nil || err != nil {
				c.Printf("Unknown help topic %#q\n", args)
				_ = c.Root().Usage()
				return
			}
			target.InitDefaultHelpFlag()
			target.InitDefaultVersionFlag()
			_ = target.Help()
		},
	}
	help.AddCommand(&cobra.Command{
		Use:   "contract",
		Short: "Print the contract a project follows to adopt lanes, as markdown",
		Long: `Print the contract a project follows to adopt lanes, as markdown on stdout:
the contract of this installed version, the same text as the one published
with its release. It needs no network.`,
		Example: `  lanes help contract
  lanes help contract | less`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := io.WriteString(cmd.OutOrStdout(), contract)
			return err
		},
	})
	return help
}

func cwd() (string, error) {
	return os.Getwd()
}

func daemonReady(ctx context.Context) (string, error) {
	if !client.Ping(ctx) {
		return "", client.ErrDaemonDown
	}
	return pc.Resolve(ctx)
}

func prepare(ctx context.Context, w *workspace.Workspace, log io.Writer) error {
	if err := prepareCore(ctx, w, log); err != nil {
		return err
	}
	return generateFirebase(ctx, w, log)
}

func generateFirebase(ctx context.Context, w *workspace.Workspace, log io.Writer) error {
	return firebase.Generate(ctx, w.WT.Root, w.Cfg, w.Block.Start, log)
}

func prepareCore(ctx context.Context, w *workspace.Workspace, log io.Writer) error {
	if err := prep.Run(ctx, prep.Options{
		Worktree: w.WT,
		MainRoot: w.MainRoot,
		Config:   w.Cfg,
		Environ:  w.Environ(os.Environ()),
		Log:      log,
	}); err != nil {
		return err
	}
	return generate.Run(ctx, w.WT.Root, w.Cfg.Generate, w.Vars.Map(), log)
}

func settle(ctx context.Context, dir string, log io.Writer) (*workspace.Workspace, error) {
	w, err := workspace.Settle(ctx, dir)
	if err != nil {
		return nil, err
	}
	for _, d := range w.Deferred {
		fmt.Fprintf(log, "lanes: %v — kept registered, lanesd erases it once it can\n", d)
	}
	return w, nil
}

func withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d)
}

// Quiet errors carry only an exit status: the command already spoke.
func Quiet(err error) bool {
	var q *exitCodeError
	return errors.As(err, &q)
}

func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var code interface{ ExitCode() int }
	if errors.As(err, &code) {
		return code.ExitCode()
	}
	return 1
}

func printf(w io.Writer, format string, a ...any) {
	fmt.Fprintf(w, format, a...)
}
