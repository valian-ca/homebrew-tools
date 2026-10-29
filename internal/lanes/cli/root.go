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
	"github.com/valian-ca/homebrew-tools/internal/lanes/generate"
	"github.com/valian-ca/homebrew-tools/internal/lanes/pc"
	"github.com/valian-ca/homebrew-tools/internal/lanes/prep"
	"github.com/valian-ca/homebrew-tools/internal/lanes/workspace"
)

func NewRoot(version string) *cobra.Command {
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
versioned. See the contract: https://github.com/valian-ca/homebrew-tools/blob/main/cmd/lanes/README.md`,
		Example: `  cd ~/code/acme/.claude/worktrees/fix-login
  lanes start              # start this worktree's stack, wait until it is ready
  eval "$(lanes env)"      # its ports and TMPDIR in this shell
  lanes list --json        # every lane on this Mac`,
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
	)
	return root
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

func withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d)
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
