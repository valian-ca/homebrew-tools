package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/valian-ca/homebrew-tools/internal/lanes/client"
	"github.com/valian-ca/homebrew-tools/internal/lanes/proto"
	"github.com/valian-ca/homebrew-tools/internal/lanes/workspace"
)

const sessionEndBudget = 500 * time.Millisecond

type hookPayload struct {
	Cwd string `json:"cwd"`
}

func newHookCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "hook",
		Short: "Entry points for the project's Claude Code hooks",
		Long: `Entry points for the project's Claude Code hooks. Each reads the hook's JSON
payload on stdin and works on the directory the payload names — the worktree,
even when Claude Desktop runs the hook from the main checkout.

Hooks never block a session: they exit 0 and report problems on stderr. On a
main checkout, or without lanes.yaml, they do nothing.

Wire them as the contract shows, guarded so a Mac without lanes stays silent:
  command -v lanes >/dev/null 2>&1 && lanes hook session-start || true`,
	}
	c.AddCommand(
		envHook("session-start", "SessionStart: settle the lane, run prep steps, export the lane's variables"),
		envHook("cwd-changed", "CwdChanged: settle the lane of the new directory, run prep steps, export its variables"),
		&cobra.Command{
			Use:   "session-end",
			Short: "SessionEnd: tell lanesd a session ended, in well under a second",
			Long: `Tell lanesd that a session ended in the payload's directory. lanesd stops the
stack at once if no session remains. Returns in well under a second, and
silently when lanesd is not running.`,
			Example: `  echo '{"cwd":"'"$PWD"'"}' | lanes hook session-end`,
			Args:    cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				dir := payloadDir(cmd.InOrStdin())
				ctx, cancel := context.WithTimeout(cmd.Context(), sessionEndBudget)
				defer cancel()
				_, _ = client.Call(ctx, proto.Request{Op: proto.OpNotify, Worktree: dir})
				return nil
			},
		},
	)
	return c
}

func envHook(name, short string) *cobra.Command {
	return &cobra.Command{
		Use:   name,
		Short: short,
		Long: short + `.

The variables are appended to $CLAUDE_ENV_FILE, so every Bash command of the
session sees the lane's ports and TMPDIR, over the TMPDIR Claude Code sets.`,
		Example: `  echo '{"cwd":"'"$PWD"'"}' | lanes hook ` + name,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := runEnvHook(cmd.Context(), payloadDir(cmd.InOrStdin()), cmd.ErrOrStderr()); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "lanes: %v\n", err)
			}
			return nil
		},
	}
}

func runEnvHook(ctx context.Context, dir string, log io.Writer) error {
	w, err := workspace.Open(ctx, dir)
	if workspace.IsQuiet(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := prepare(ctx, w, log); err != nil {
		return err
	}
	file := os.Getenv("CLAUDE_ENV_FILE")
	if file == "" {
		return nil
	}
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(w.Vars.Exports()); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// payloadDir reads the hook payload's cwd, falling back to the process cwd
// when run by hand from a terminal or given no payload.
func payloadDir(in io.Reader) string {
	fallback, _ := os.Getwd()
	if f, ok := in.(*os.File); ok && isTerminal(f) {
		return fallback
	}
	var p hookPayload
	if err := json.NewDecoder(io.LimitReader(in, 1<<20)).Decode(&p); err != nil || p.Cwd == "" {
		return fallback
	}
	return p.Cwd
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
