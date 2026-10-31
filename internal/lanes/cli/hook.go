package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/valian-ca/homebrew-tools/internal/lanes/client"
	"github.com/valian-ca/homebrew-tools/internal/lanes/env"
	"github.com/valian-ca/homebrew-tools/internal/lanes/fsx"
	"github.com/valian-ca/homebrew-tools/internal/lanes/paths"
	"github.com/valian-ca/homebrew-tools/internal/lanes/ports"
	"github.com/valian-ca/homebrew-tools/internal/lanes/proto"
	"github.com/valian-ca/homebrew-tools/internal/lanes/workspace"
)

const sessionEndBudget = 500 * time.Millisecond

type hookPayload struct {
	Cwd           string `json:"cwd"`
	SessionID     string `json:"session_id"`
	HookEventName string `json:"hook_event_name"`
}

var sessionID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func newHookCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "hook",
		Short: "Entry points for the project's Claude Code hooks",
		Long: `Entry points for the project's Claude Code hooks. Each reads the hook's JSON
payload on stdin and works on the directory the payload names — the worktree,
even when Claude Desktop runs the hook from the main checkout.

In a worktree, session-start adds the lane's context block to the session:
its lane, worktree, host, stack state, ports and commands. On a main checkout
it adds a two-line note and exports nothing. cwd-changed adds the same text
after EnterWorktree or ExitWorktree, never under CwdChanged, from which Claude
Code takes no context. Without lanes.yaml, the hooks add nothing.

Hooks never block a session: they exit 0 and report problems on stderr.

Wire them as lanes help contract shows, guarded so a Mac without lanes stays
silent:
  command -v lanes >/dev/null 2>&1 && lanes hook session-start || true`,
	}
	c.AddCommand(
		envHook("session-start", "SessionStart: settle the lane, run prep steps, export the lane's variables, add its context block"),
		envHook("cwd-changed", "CwdChanged, and PostToolUse after EnterWorktree or ExitWorktree: settle the lane of the new directory, run prep steps, export its variables; after EnterWorktree or ExitWorktree, add its context block"),
		&cobra.Command{
			Use:   "session-end",
			Short: "SessionEnd: tell lanesd a session ended, in well under a second",
			Long: `Tell lanesd that a session ended in the payload's directory. lanesd stops the
stack at once if no session remains. Returns in well under a second, and
silently when lanesd is not running.`,
			Example: `  echo '{"cwd":"'"$PWD"'"}' | lanes hook session-end`,
			Args:    cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				p := readPayload(cmd.InOrStdin())
				ctx, cancel := context.WithTimeout(cmd.Context(), sessionEndBudget)
				defer cancel()
				for _, dir := range sessionWorktrees(p) {
					_, _ = client.Call(ctx, proto.Request{Op: proto.OpNotify, Worktree: dir})
				}
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
session sees the lane's ports and TMPDIR, over the TMPDIR Claude Code sets.

The context — the lane's block in a worktree, a two-line note on a main
checkout — goes to stdout as the hook's JSON additionalContext. It never
starts the stack.`,
		Example: `  echo '{"cwd":"'"$PWD"'"}' | lanes hook ` + name,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p := readPayload(cmd.InOrStdin())
			event := contextEvent(name, p)
			text, err := runEnvHook(cmd.Context(), p, event != "", cmd.ErrOrStderr())
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "lanes: %v\n", err)
			}
			if text != "" {
				if err := writeContext(cmd.OutOrStdout(), event, text); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "lanes: %v\n", err)
				}
			}
			return nil
		},
	}
}

type sessionEnv struct {
	Files  []string `json:"files"`
	Tmpdir string   `json:"tmpdir"`
	Vars   []string `json:"vars,omitempty"`
}

// staleSession bounds what a killed session, which never runs SessionEnd,
// leaves in the state dir.
const staleSession = 30 * 24 * time.Hour

// runEnvHook rewrites every env file Claude Code gave the session's lanes
// hooks: within one file the first value of a variable wins, so appending
// could never move a session to another lane. A hook given no file, such as
// PostToolUse after EnterWorktree, still reaches the session's.
func runEnvHook(ctx context.Context, p hookPayload, withContext bool, log io.Writer) (string, error) {
	known := sessionID.MatchString(p.SessionID)
	var se sessionEnv
	if known {
		_, _ = fsx.ReadJSON(paths.SessionEnv(p.SessionID), &se)
	}
	if f := os.Getenv("CLAUDE_ENV_FILE"); f != "" && !slices.Contains(se.Files, f) {
		if len(se.Files) == 0 {
			se.Tmpdir = os.Getenv("TMPDIR")
		}
		se.Files = append(se.Files, f)
	}
	w, err := settle(ctx, p.Cwd, log)
	var (
		text   string
		genErr error
	)
	switch {
	case workspace.IsQuiet(err):
		if withContext {
			text = quietContext(ctx, p.Cwd, err)
		}
		if len(se.Vars) > 0 {
			if err := rewrite(se.Files, cleared(se.Vars, se.Tmpdir)); err != nil {
				return text, err
			}
			se.Vars = nil
		}
	case err != nil:
		if withContext {
			text = unsettledContextFor(ctx, p.Cwd, err)
		}
		return text, err
	default:
		var envErr error
		if w.EnvErr != nil {
			envErr = fmt.Errorf("%w — the env: variables are left out", w.EnvErr)
			fmt.Fprintf(log, "lanes: %v\n", envErr)
		}
		block := func(problems ...error) string {
			if !withContext {
				return ""
			}
			return laneContext(w, stackState(ctx, w), problems...)
		}
		if known {
			if err := fsx.WriteAtomic(paths.SessionMark(w.WT.AdminDir, p.SessionID), nil, paths.FileMode); err != nil {
				return block(notExported(err), envErr), err
			}
		}
		if err := prepareCore(ctx, w, log); err != nil {
			return block(notExported(problem(err)), envErr), err
		}
		genErr = generateFirebase(ctx, w, log)
		if err := rewrite(se.Files, w.Vars.Exports()); err != nil {
			return block(notExported(err), envErr, genErr), err
		}
		se.Vars = w.Vars.Names()
		text = block(envErr, genErr)
	}
	if !known || len(se.Files) == 0 {
		return text, genErr
	}
	pruneSessions()
	return text, errors.Join(genErr, fsx.WriteJSON(paths.SessionEnv(p.SessionID), se))
}

func notExported(err error) error {
	return fmt.Errorf("%w — the lane's variables are not exported", err)
}

// contextEvent is empty under CwdChanged: Claude Code takes no context from
// that event.
func contextEvent(hook string, p hookPayload) string {
	switch {
	case hook == "session-start":
		return "SessionStart"
	case p.HookEventName == "PostToolUse":
		return "PostToolUse"
	}
	return ""
}

// cleared exports each variable empty, TMPDIR back to the session's own:
// Claude Code applies only the export lines of its env files, never an unset.
func cleared(names []string, tmpdir string) string {
	var b strings.Builder
	for _, n := range names {
		v := ""
		if n == "TMPDIR" {
			v = tmpdir
		}
		fmt.Fprintf(&b, "export %s=%s\n", n, env.Quote(v))
	}
	return b.String()
}

func rewrite(files []string, text string) error {
	for _, f := range files {
		if err := os.WriteFile(f, []byte(text), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func pruneSessions() {
	dir := paths.Sessions()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > staleSession {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// readPayload falls back to the process cwd when run by hand from a terminal
// or given no payload.
func readPayload(in io.Reader) hookPayload {
	var p hookPayload
	if f, ok := in.(*os.File); !ok || !isTerminal(f) {
		_ = json.NewDecoder(io.LimitReader(in, 1<<20)).Decode(&p)
	}
	if p.Cwd == "" {
		p.Cwd, _ = os.Getwd()
	}
	return p
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// sessionWorktrees adds the worktrees the session's start hooks marked: a
// session opened by claude --worktree has already left its worktree when
// SessionEnd fires, so the payload's cwd is the main checkout.
func sessionWorktrees(p hookPayload) []string {
	dirs := []string{p.Cwd}
	if !sessionID.MatchString(p.SessionID) {
		return dirs
	}
	_ = os.Remove(paths.SessionEnv(p.SessionID))
	t, err := ports.Load()
	if err != nil {
		return dirs
	}
	for _, e := range t.Entries {
		if os.Remove(paths.SessionMark(e.AdminDir, p.SessionID)) == nil && e.Worktree != p.Cwd {
			dirs = append(dirs, e.Worktree)
		}
	}
	return dirs
}
