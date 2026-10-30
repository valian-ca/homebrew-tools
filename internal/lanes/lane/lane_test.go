package lane

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/valian-ca/homebrew-tools/internal/lanes/config"
	"github.com/valian-ca/homebrew-tools/internal/lanes/gitx"
	"github.com/valian-ca/homebrew-tools/internal/lanes/testutil"
)

func TestLongestIDIsTheOneConfigBudgets(t *testing.T) {
	if got := len(NewID(strings.Repeat("x", 100))); got != config.MaxLaneIDLen {
		t.Fatalf("the longest lane ID has %d characters, lanes.yaml budgets %d for database names", got, config.MaxLaneIDLen)
	}
}

func TestSlugAndID(t *testing.T) {
	cases := map[string]string{
		"feature/val-369-noyau":                         "feature_val_369_noyau",
		"Fix/ÉTÉ--crash":                                "fix_t_crash",
		"claude/a-very-long-branch-name-that-overflows": "claude_a_very_long_branc",
		"///": "lane",
	}
	for branch, want := range cases {
		if got := Slug(branch); got != want {
			t.Errorf("Slug(%q) = %q, want %q", branch, got, want)
		}
	}
	id := NewID("feature/val-369-noyau")
	if !regexp.MustCompile(`^feature_val_369_noyau_[0-9a-f]{6}$`).MatchString(id) {
		t.Errorf("NewID = %q", id)
	}
	if len(Slug(strings.Repeat("x", 100))) != slugMax {
		t.Errorf("slug not truncated to %d", slugMax)
	}
}

type fixture struct {
	t       *testing.T
	main    string
	wt      gitx.Worktree
	retired []Record
}

func newFixture(t *testing.T, branch string) *fixture {
	testutil.Hermetic(t)
	main := testutil.Repo(t)
	dir := testutil.Worktree(t, main, "wt", branch)
	wt, err := gitx.Detect(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, main: main, wt: wt}
}

func (f *fixture) check() (Record, bool) {
	f.t.Helper()
	rec, born, err := Check(context.Background(), f.wt, func(_ context.Context, old Record) error {
		f.retired = append(f.retired, old)
		return nil
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return rec, born
}

func (f *fixture) git(args ...string) string {
	f.t.Helper()
	return testutil.Git(f.t, f.wt.Root, args...)
}

func (f *fixture) advanceOrigin(file string) {
	f.t.Helper()
	testutil.Commit(f.t, f.main, file, file+"\n")
	testutil.Git(f.t, f.main, "push", "-q", "origin", "main")
	f.git("fetch", "-q", "origin")
}

func TestFirstPassBirthsALane(t *testing.T) {
	f := newFixture(t, "feat/one")
	rec, born := f.check()
	if !born || !strings.HasPrefix(rec.ID, "feat_one_") || rec.Branch != "feat/one" {
		t.Fatalf("first pass: born=%v rec=%+v", born, rec)
	}
	again, born := f.check()
	if born || again.ID != rec.ID {
		t.Fatalf("second pass changed the lane: %+v", again)
	}
	if len(f.retired) != 0 {
		t.Fatalf("retired %v on a first birth", f.retired)
	}
}

func TestDetachedHeadKeepsTheLane(t *testing.T) {
	f := newFixture(t, "feat/archive")
	rec, _ := f.check()
	f.git("checkout", "-q", "--detach")
	got, born := f.check()
	if born || got.ID != rec.ID {
		t.Fatalf("detached HEAD gave a new lane: %+v", got)
	}
	f.git("checkout", "-q", "feat/archive")
	if got, born := f.check(); born || got.ID != rec.ID {
		t.Fatalf("reattaching gave a new lane: %+v", got)
	}
}

func TestRenameChainThenRebaseKeepsTheLane(t *testing.T) {
	f := newFixture(t, "feat/a")
	testutil.Commit(t, f.wt.Root, "work.txt", "work\n")
	rec, _ := f.check()

	f.git("branch", "-m", "feat/b")
	f.git("branch", "-m", "feat/c")
	f.advanceOrigin("upstream.txt")
	f.git("rebase", "-q", "origin/main")

	got, born := f.check()
	if born || got.ID != rec.ID || got.Branch != "feat/c" {
		t.Fatalf("rename + rebase: born=%v got=%+v want id %s", born, got, rec.ID)
	}
}

func TestUnrelatedBranchRetiresTheOldLane(t *testing.T) {
	f := newFixture(t, "feat/old")
	old, _ := f.check()
	f.git("switch", "-q", "-c", "feat/unrelated", "origin/main")
	got, born := f.check()
	if !born || got.ID == old.ID || !strings.HasPrefix(got.ID, "feat_unrelated_") {
		t.Fatalf("unrelated branch: born=%v got=%+v", born, got)
	}
	if len(f.retired) != 1 || f.retired[0].ID != old.ID {
		t.Fatalf("retired = %+v, want the old lane", f.retired)
	}
}

func TestResetToMainAfterMergeBirthsALane(t *testing.T) {
	f := newFixture(t, "feat/merged")
	testutil.Commit(t, f.wt.Root, "feature.txt", "feature\n")
	old, _ := f.check()

	testutil.Git(t, f.main, "merge", "-q", "--squash", "feat/merged")
	testutil.Git(t, f.main, "commit", "-q", "-m", "squash feat/merged")
	testutil.Git(t, f.main, "push", "-q", "origin", "main")
	f.git("fetch", "-q", "origin")
	f.git("reset", "-q", "--hard", "origin/main")

	got, born := f.check()
	if !born || got.ID == old.ID {
		t.Fatalf("reset to main after merge kept the lane: %+v", got)
	}
}

func TestResetToMainAfterMergeCommitBirthsALane(t *testing.T) {
	f := newFixture(t, "feat/merged")
	testutil.Commit(t, f.wt.Root, "feature.txt", "feature\n")
	old, _ := f.check()

	testutil.Git(t, f.main, "merge", "-q", "--no-ff", "-m", "Merge feat/merged", "feat/merged")
	testutil.Git(t, f.main, "push", "-q", "origin", "main")
	f.git("fetch", "-q", "origin")
	if got, born := f.check(); born || got.ID != old.ID {
		t.Fatalf("a fetched merge changed the lane before any reset: %+v", got)
	}
	f.git("reset", "-q", "--hard", "origin/main")

	got, born := f.check()
	if !born || got.ID == old.ID {
		t.Fatalf("reset to main after a merge commit kept the lane: %+v", got)
	}
}

func TestMergedBranchLeftAloneKeepsTheLane(t *testing.T) {
	f := newFixture(t, "feat/merged")
	testutil.Commit(t, f.wt.Root, "feature.txt", "feature\n")
	rec, _ := f.check()

	testutil.Git(t, f.main, "merge", "-q", "--no-ff", "-m", "Merge feat/merged", "feat/merged")
	testutil.Git(t, f.main, "push", "-q", "origin", "main")
	f.git("fetch", "-q", "origin")
	f.advanceOrigin("upstream.txt")
	for range 2 {
		if got, born := f.check(); born || got.ID != rec.ID {
			t.Fatalf("a merged branch left untouched changed lane: %+v", got)
		}
	}
}

func TestBranchWithoutCommitsFollowingMainKeepsTheLane(t *testing.T) {
	f := newFixture(t, "feat/idle")
	rec, _ := f.check()
	f.advanceOrigin("upstream.txt")
	f.git("reset", "-q", "--hard", "origin/main")
	if got, born := f.check(); born || got.ID != rec.ID {
		t.Fatalf("an idle branch following main changed lane: %+v", got)
	}
}

func TestMarkCreated(t *testing.T) {
	f := newFixture(t, "feat/res")
	rec, _ := f.check()
	if err := MarkCreated(f.wt, rec.ID, "store"); err != nil {
		t.Fatal(err)
	}
	if err := MarkCreated(f.wt, rec.ID, "store"); err != nil {
		t.Fatal(err)
	}
	got, _ := f.check()
	if strings.Join(got.Created, ",") != "store" {
		t.Fatalf("created = %v", got.Created)
	}
}

func TestMarkStarted(t *testing.T) {
	f := newFixture(t, "feat/started")
	rec, _ := f.check()
	if rec.StartedAt != nil {
		t.Fatalf("a new lane is already started: %v", rec.StartedAt)
	}
	if err := MarkStarted(f.wt, "other_lane_000000"); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.check(); got.StartedAt != nil {
		t.Fatal("another lane's start marked this one")
	}
	if err := MarkStarted(f.wt, rec.ID); err != nil {
		t.Fatal(err)
	}
	first, _ := f.check()
	if first.StartedAt == nil {
		t.Fatal("the start was not recorded")
	}
	if err := MarkStarted(f.wt, rec.ID); err != nil {
		t.Fatal(err)
	}
	if again, _ := f.check(); !again.StartedAt.Equal(*first.StartedAt) {
		t.Fatalf("a second start moved the mark: %v, then %v", first.StartedAt, again.StartedAt)
	}
	f.git("switch", "-q", "-c", "feat/other")
	if next, born := f.check(); !born || next.StartedAt != nil {
		t.Fatalf("a new lane inherits the start: born=%v %+v", born, next)
	}
}

func TestSetHost(t *testing.T) {
	f := newFixture(t, "feat/host")
	rec, _ := f.check()
	if err := SetHost(f.wt, rec.ID, "192.168.1.20"); err != nil {
		t.Fatal(err)
	}
	got, err := Load(f.wt.AdminDir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Host != "192.168.1.20" {
		t.Fatalf("host = %q, want 192.168.1.20", got.Host)
	}
	if err := SetHost(f.wt, rec.ID, ""); err != nil {
		t.Fatal(err)
	}
	got, err = Load(f.wt.AdminDir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Host != "" {
		t.Fatalf("host = %q, want empty", got.Host)
	}
	err = SetHost(f.wt, "other_lane_000000", "10.0.0.1")
	if err == nil || !strings.Contains(err.Error(), "is no longer this worktree's lane") {
		t.Fatalf("SetHost on another lane: err = %v", err)
	}
	got, err = Load(f.wt.AdminDir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Host != "" {
		t.Fatalf("host changed by a stale lane: %q", got.Host)
	}
}
