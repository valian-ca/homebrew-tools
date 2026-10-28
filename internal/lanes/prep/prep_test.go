package prep

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/valian-ca/homebrew-tools/internal/lanes/config"
	"github.com/valian-ca/homebrew-tools/internal/lanes/gitx"
	"github.com/valian-ca/homebrew-tools/internal/lanes/testutil"
)

type fixture struct {
	main string
	wt   gitx.Worktree
}

func newFixture(t *testing.T) fixture {
	testutil.Hermetic(t)
	main := testutil.Repo(t)
	testutil.Write(t, filepath.Join(main, ".worktreeinclude"), "# local files\nsecrets.local\nconf/*.local\n!conf/skip.local\n../outside\n")
	testutil.Write(t, filepath.Join(main, ".env.local.template"), "A=from-template\n")
	testutil.Write(t, filepath.Join(main, "api/.env.test.template"), "B=1\n")
	testutil.Write(t, filepath.Join(main, "notenv.template"), "C=1\n")
	testutil.Write(t, filepath.Join(main, ".gitignore"), "*.local\n.env.local\n.env.test\n")
	testutil.Git(t, main, "add", "-A")
	testutil.Git(t, main, "commit", "-q", "-m", "prep fixtures")
	testutil.Write(t, filepath.Join(main, "secrets.local"), "S=main\n")
	testutil.Write(t, filepath.Join(main, "conf/a.local"), "a\n")
	testutil.Write(t, filepath.Join(main, "conf/skip.local"), "skip\n")
	if err := os.Symlink("/etc/hosts", filepath.Join(main, "conf/link.local")); err != nil {
		t.Fatal(err)
	}
	dir := testutil.Worktree(t, main, "wt", "feat/prep")
	wt, err := gitx.Detect(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{main: main, wt: wt}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return "<missing>"
	}
	return string(data)
}

func TestBuiltinsCopyAndCreateWithoutOverwriting(t *testing.T) {
	f := newFixture(t)
	root := f.wt.Root
	testutil.Write(t, filepath.Join(root, "conf/a.local"), "kept\n")

	o := Options{Worktree: f.wt, MainRoot: f.main, Config: &config.Config{}}
	if err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	checks := map[string]string{
		"secrets.local":   "S=main\n",
		"conf/a.local":    "kept\n",
		"conf/skip.local": "<missing>",
		"conf/link.local": "<missing>",
		".env.local":      "A=from-template\n",
		"api/.env.test":   "B=1\n",
		"notenv":          "<missing>",
	}
	for rel, want := range checks {
		if got := read(t, filepath.Join(root, rel)); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(root), "outside")); !errors.Is(err, os.ErrNotExist) {
		t.Error("a pattern outside the tree was followed")
	}
}

func TestStepRerunsOnlyWhenItsTriggerChanges(t *testing.T) {
	f := newFixture(t)
	root := f.wt.Root
	testutil.Write(t, filepath.Join(root, "deps.lock"), "v1\n")
	cfg := &config.Config{Setup: []config.Step{
		{Name: "deps", Run: "echo run >> .runs-deps", WhenChanged: []string{"deps.lock"}},
		{Name: "once", Run: "echo run >> .runs-once"},
	}}
	o := Options{Worktree: f.wt, MainRoot: f.main, Config: cfg, Environ: os.Environ()}
	runs := func(name string) int { return strings.Count(read(t, filepath.Join(root, ".runs-"+name)), "run") }

	for range 2 {
		if err := Run(context.Background(), o); err != nil {
			t.Fatal(err)
		}
	}
	if runs("deps") != 1 || runs("once") != 1 {
		t.Fatalf("after two passes: deps=%d once=%d, want 1 and 1", runs("deps"), runs("once"))
	}
	testutil.Write(t, filepath.Join(root, "deps.lock"), "v2\n")
	if err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if runs("deps") != 2 || runs("once") != 1 {
		t.Fatalf("after a trigger change: deps=%d once=%d, want 2 and 1", runs("deps"), runs("once"))
	}
}

func TestFailingStepKeepsItsStampAndReportsItsOutput(t *testing.T) {
	f := newFixture(t)
	cfg := &config.Config{Setup: []config.Step{{Name: "boom", Run: "echo installing; echo broken >&2; exit 4"}}}
	var log bytes.Buffer
	err := Run(context.Background(), Options{Worktree: f.wt, MainRoot: f.main, Config: cfg, Log: &log})
	var se *StepError
	if !errors.As(err, &se) || se.Step != "boom" || !strings.Contains(se.Tail, "broken") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "prep step boom failed") {
		t.Errorf("message = %q", err)
	}
	if err := Run(context.Background(), Options{Worktree: f.wt, MainRoot: f.main, Config: cfg}); err == nil {
		t.Fatal("a failed step was stamped as done")
	}
}
