package cmds

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// NewWorktreeModeCmd builds the `atelierd worktree-mode` sub-command.
func NewWorktreeModeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "worktree-mode",
		Short: "Print whether the cwd is a primary checkout or a linked worktree",
		Long: `Detect whether the current directory belongs to a repository's primary
checkout or to a linked worktree (git worktree add), and print exactly one of:

  primary
  worktree

Comparison: a linked worktree's --git-dir points inside the primary's
.git/worktrees/<name>, while --git-common-dir points at the primary .git —
they only coincide in the primary checkout.

Exits non-zero without printing a mode when the cwd is not inside a git
repository.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mode, err := detectWorktreeMode(cmd.Context(), "")
			if err != nil {
				return err
			}
			cmd.Println(mode)
			return nil
		},
	}
}

// detectWorktreeMode reports "primary" or "worktree" for the repository
// containing dir (the process cwd when dir is empty).
func detectWorktreeMode(ctx context.Context, dir string) (string, error) {
	gitDir, err := gitRevParseDir(ctx, dir, "--git-dir")
	if err != nil {
		return "", err
	}
	commonDir, err := gitRevParseDir(ctx, dir, "--git-common-dir")
	if err != nil {
		return "", err
	}
	if gitDir == commonDir {
		return "primary", nil
	}
	return "worktree", nil
}

// gitRevParseDir runs `git rev-parse <flag>` in dir and returns the printed
// path as an absolute path. Anchoring is mandatory before comparing: at a
// primary checkout's root git prints the relative ".git" for --git-dir while
// other invocations come back absolute, so raw string equality would
// misclassify.
func gitRevParseDir(ctx context.Context, dir, flag string) (string, error) {
	c := exec.CommandContext(ctx, "git", "rev-parse", flag)
	c.Dir = dir
	out, err := c.Output()
	if err != nil {
		// Surface git's own diagnostic ("not a git repository...") instead of
		// a bare exit status so the failure is actionable from a hook log.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return "", fmt.Errorf("git rev-parse %s: %s", flag, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("git rev-parse %s: %w", flag, err)
	}

	path := strings.TrimSpace(string(out))
	if path == "" {
		return "", fmt.Errorf("git rev-parse %s: empty output", flag)
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	// Relative output is relative to the subprocess's working directory,
	// not necessarily this process's cwd — anchor against the same dir the
	// command ran in.
	base := dir
	if base == "" {
		base, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	return filepath.Join(base, path), nil
}
