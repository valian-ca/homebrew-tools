package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/valian-ca/homebrew-tools/internal/lanes/testutil"
)

func runInit(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	t.Chdir(dir)
	root := NewRoot("test", "")
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"init"}, args...))
	err := root.Execute()
	return out.String(), err
}

func initFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	testutil.Hermetic(t)
	dir := testutil.RealTemp(t)
	testutil.Git(t, dir, "init", "-q", "-b", "main")
	for p, c := range files {
		testutil.Write(t, filepath.Join(dir, p), c)
	}
	testutil.Git(t, dir, "add", "-A")
	testutil.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	return dir
}

func TestInitRefusesWithAndWithoutDryRun(t *testing.T) {
	for _, name := range []string{"lanes.yaml", "process-compose.yaml"} {
		for _, args := range [][]string{nil, {"--dry-run"}} {
			dir := initFixture(t, map[string]string{name: "x: 1\n", "package-lock.json": "{}"})
			out, err := runInit(t, dir, args...)
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Errorf("lanes init %v with %s: %v\n%s", args, name, err, out)
			}
			if s := testutil.Git(t, dir, "status", "--porcelain", "--untracked-files=all"); s != "" {
				t.Errorf("lanes init %v with %s changed the tree:\n%s", args, name, s)
			}
		}
	}
}

func TestInitDryRun(t *testing.T) {
	dir := initFixture(t, map[string]string{"firebase.json": `{"emulators": {"auth": {}}}`})
	out, err := runInit(t, dir, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if s := testutil.Git(t, dir, "status", "--porcelain", "--untracked-files=all"); s != "" {
		t.Fatalf("a dry run changed the tree:\n%s", s)
	}
	for _, w := range []string{"== lanes.yaml (write) ==", "== process-compose.yaml (write) ==", "== .claude/settings.json (write) ==", "== .envrc (write) ==", "== .gitignore (write) ==", "nothing written", "To fill in"} {
		if !strings.Contains(out, w) {
			t.Errorf("the dry run lacks %q:\n%s", w, out)
		}
	}
	if _, err := runInit(t, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "lanes.yaml")); err != nil {
		t.Errorf("lanes init wrote no lanes.yaml: %v", err)
	}
}
