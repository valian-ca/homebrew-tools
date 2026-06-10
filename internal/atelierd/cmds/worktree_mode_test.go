package cmds

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runGit executes git in dir with a hermetic config so the developer's
// global/system gitconfig (signing hooks, templates, default branch) can't
// affect the fixture repos.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// initPrimaryRepo creates a repo with one commit — `git worktree add`
// requires a HEAD to base the linked tree on.
func initPrimaryRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	return dir
}

func TestDetectWorktreeMode_Primary(t *testing.T) {
	t.Parallel()
	primary := initPrimaryRepo(t)

	got, err := detectWorktreeMode(context.Background(), primary)
	if err != nil {
		t.Fatalf("detectWorktreeMode: %v", err)
	}
	if got != "primary" {
		t.Fatalf("detectWorktreeMode in primary checkout = %q, want %q", got, "primary")
	}
}

func TestDetectWorktreeMode_LinkedWorktree(t *testing.T) {
	t.Parallel()
	primary := initPrimaryRepo(t)
	linked := filepath.Join(t.TempDir(), "linked")
	// --detach avoids creating a branch whose name could collide with the
	// fixture's default branch.
	runGit(t, primary, "worktree", "add", "-q", "--detach", linked)

	got, err := detectWorktreeMode(context.Background(), linked)
	if err != nil {
		t.Fatalf("detectWorktreeMode: %v", err)
	}
	if got != "worktree" {
		t.Fatalf("detectWorktreeMode in linked worktree = %q, want %q", got, "worktree")
	}

	// The primary must still classify as primary with a linked tree attached.
	got, err = detectWorktreeMode(context.Background(), primary)
	if err != nil {
		t.Fatalf("detectWorktreeMode: %v", err)
	}
	if got != "primary" {
		t.Fatalf("detectWorktreeMode in primary (with linked tree) = %q, want %q", got, "primary")
	}
}

func TestDetectWorktreeMode_NotARepo(t *testing.T) {
	t.Parallel()
	_, err := detectWorktreeMode(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("detectWorktreeMode outside a repo: want error, got nil")
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("error %q does not surface git's diagnostic", err)
	}
}

func TestWorktreeModeCmd_PrintsPrimary(t *testing.T) {
	primary := initPrimaryRepo(t)
	t.Chdir(primary)

	c := NewWorktreeModeCmd()
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetArgs([]string{})
	if err := c.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.String() != "primary\n" {
		t.Fatalf("stdout = %q, want %q", out.String(), "primary\n")
	}
}

func TestWorktreeModeCmd_NotARepo_PrintsNoMode(t *testing.T) {
	t.Chdir(t.TempDir())

	c := NewWorktreeModeCmd()
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&bytes.Buffer{})
	c.SilenceErrors = true
	c.SilenceUsage = true
	c.SetArgs([]string{})
	if err := c.Execute(); err == nil {
		t.Fatal("Execute outside a repo: want error, got nil")
	}
	// A bogus mode on stdout would poison callers that pipe the output.
	if out.Len() != 0 {
		t.Fatalf("stdout = %q, want empty on failure", out.String())
	}
}
