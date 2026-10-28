package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// GitEnv keeps the developer's git configuration (signing, hooks, default
// branch) out of fixture repositories.
var GitEnv = []string{
	"GIT_CONFIG_GLOBAL=/dev/null",
	"GIT_CONFIG_SYSTEM=/dev/null",
	"GIT_AUTHOR_NAME=test",
	"GIT_AUTHOR_EMAIL=test@example.com",
	"GIT_COMMITTER_NAME=test",
	"GIT_COMMITTER_EMAIL=test@example.com",
}

// Hermetic makes every git command of the test process, the code under test
// included, ignore the developer's configuration.
func Hermetic(t *testing.T) {
	t.Helper()
	for _, kv := range GitEnv {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
}

func Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), GitEnv...)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// Repo creates a main checkout with one commit on main, pushed to a bare
// origin so that origin/main and origin/HEAD exist.
func Repo(t *testing.T) string {
	t.Helper()
	base := RealTemp(t)
	main := filepath.Join(base, "repo")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	Git(t, main, "init", "-q", "-b", "main")
	Write(t, filepath.Join(main, "README"), "demo\n")
	Git(t, main, "add", "-A")
	Git(t, main, "commit", "-q", "-m", "init")
	origin := filepath.Join(base, "origin.git")
	Git(t, base, "init", "-q", "--bare", origin)
	Git(t, main, "remote", "add", "origin", origin)
	Git(t, main, "push", "-q", "-u", "origin", "main")
	Git(t, main, "remote", "set-head", "origin", "main")
	return main
}

func Worktree(t *testing.T, main, name, branch string) string {
	t.Helper()
	dir := filepath.Join(filepath.Dir(main), name)
	Git(t, main, "worktree", "add", "-q", "-b", branch, dir)
	return dir
}

func Commit(t *testing.T, dir, file, content string) string {
	t.Helper()
	Write(t, filepath.Join(dir, file), content)
	Git(t, dir, "add", "-A")
	Git(t, dir, "commit", "-q", "-m", "change "+file)
	return Git(t, dir, "rev-parse", "HEAD")
}

func Write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// RealTemp is a temporary directory with symlinks resolved, so paths compare
// equal to what git and the kernel report (/var is /private/var on macOS).
func RealTemp(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// StateDir isolates lanes' machine-wide state for one test.
func StateDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "lt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("LANES_TEST", "1")
	t.Setenv("LANES_STATE_DIR", dir)
	return dir
}
