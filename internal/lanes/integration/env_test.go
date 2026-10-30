//go:build integration && darwin

package integration

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func payload(cwd string) string {
	data, _ := json.Marshal(map[string]string{"cwd": cwd, "hook_event_name": "SessionStart", "session_id": "test"})
	return string(data)
}

// The hook runs from the main checkout, as Claude Desktop runs it, and still
// exports the lane of the worktree its payload names.
func TestHookExportsTheWorktreeLane(t *testing.T) {
	h := newHarness(t)
	wt := h.worktree("hook", "feat/hook")
	envFile := filepath.Join(t.TempDir(), "claude-env")

	r := h.lanesIn(h.main, payload(wt), []string{"CLAUDE_ENV_FILE=" + envFile, "TMPDIR=/var/folders/claude-imposed"}, "hook", "session-start")
	if r.code != 0 || r.stdout != "" {
		t.Fatalf("hook: %s", r)
	}
	l := h.laneOf(wt)
	c := exec.Command("/bin/bash", "-c", `export TMPDIR=/var/folders/claude-imposed; source "$1"; echo "$TMPDIR $PORT_WEB $LANE_ID"`, "_", envFile)
	out, err := c.Output()
	if err != nil {
		t.Fatal(err)
	}
	want := l.Tmpdir + " " + strconv.Itoa(l.Ports["PORT_WEB"]) + " " + l.Lane
	if strings.TrimSpace(string(out)) != want {
		t.Fatalf("a Bash command sees %q, want %q", out, want)
	}
}

func TestEnterWorktreeExportsThroughTheSessionEnvFile(t *testing.T) {
	h := newHarness(t)
	wt := h.worktree("enter", "feat/enter")
	envFile := filepath.Join(t.TempDir(), "claude-env")
	hook := func(name, cwd string, env ...string) {
		t.Helper()
		if r := h.lanesIn(h.main, `{"cwd":"`+cwd+`","session_id":"s-enter"}`, env, "hook", name); r.code != 0 {
			t.Fatalf("%s in %s: %s", name, cwd, r)
		}
	}
	sees := func() string {
		t.Helper()
		out, err := exec.Command("/bin/bash", "-c", `source "$1"; echo "$PORT_WEB $LANE_ID"`, "_", envFile).Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}

	hook("session-start", h.main, "CLAUDE_ENV_FILE="+envFile, "TMPDIR=/var/folders/session")
	hook("cwd-changed", wt)
	l := h.laneOf(wt)
	if got, want := sees(), strconv.Itoa(l.Ports["PORT_WEB"])+" "+l.Lane; got != want {
		t.Fatalf("after EnterWorktree a Bash command sees %q, want %q", got, want)
	}
	hook("cwd-changed", h.main)
	if data, _ := os.ReadFile(envFile); strings.Contains(string(data), l.Lane) {
		t.Fatalf("the env file still names the lane, and Claude Code keeps a variable's first value:\n%s", data)
	}
	if got := sees(); got != "" {
		t.Fatalf("after ExitWorktree a Bash command still sees %q", got)
	}
	tmp, err := exec.Command("/bin/bash", "-c", `source "$1"; echo "$TMPDIR"`, "_", envFile).Output()
	if err != nil || strings.TrimSpace(string(tmp)) != "/var/folders/session" {
		t.Fatalf("after ExitWorktree TMPDIR is %q, want the session's own: %v", tmp, err)
	}
}

func TestEnvUnsetLeavesNothing(t *testing.T) {
	h := newHarness(t)
	wt := h.worktree("unset", "feat/unset")
	script := `eval "$(lanes env)"; [ -n "$PORT_WEB" ] || exit 9; eval "$(lanes env --unset)"; env`
	out := h.run(wt, "/bin/bash", "-c", script)
	for _, l := range strings.Split(out, "\n") {
		name, _, _ := strings.Cut(l, "=")
		exported := strings.HasPrefix(name, "PORT_") || name == "LANES_PORT_BASE" || name == "LANES_PORT_SIZE" ||
			name == "LANE_ID" || name == "LANES_WORKTREE" || name == "LANES_VARS" || name == "TMPDIR"
		if exported {
			t.Errorf("%s survived lanes env --unset", l)
		}
	}
}

func TestPrepOnSessionStart(t *testing.T) {
	h := newHarness(t)
	h.startDaemon()
	wt := h.worktree("prep", "feat/prep")
	if err := os.WriteFile(filepath.Join(wt, ".env.local"), []byte("MINE=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	installs := filepath.Join(wt, ".demo", "installs")
	runs := func() int { return strings.Count(read(installs), "\n") }

	if r := h.lanesIn(h.main, payload(wt), nil, "hook", "session-start"); r.code != 0 || r.stderr == "" {
		t.Fatalf("hook: %s", r)
	}
	if runs() != 1 {
		t.Fatalf("the prep step ran %d times on the first session", runs())
	}
	if read(filepath.Join(wt, "secrets.local")) != "DEMO_SECRET=from-the-main-checkout\n" {
		t.Fatal(".worktreeinclude was not copied")
	}
	if read(filepath.Join(wt, ".env.local")) != "MINE=1\n" {
		t.Fatal("an existing file was overwritten")
	}

	h.mustLanes(wt, "env")
	h.session(wt)
	h.mustLanes(wt, "start")
	if runs() != 1 {
		t.Fatalf("the step reran without a trigger change (%d runs)", runs())
	}
	h.mustLanes(wt, "stop")
	if err := os.WriteFile(filepath.Join(wt, "deps.lock"), []byte("demo-deps 2.0.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.mustLanes(wt, "env")
	if runs() != 1 {
		t.Fatal("lanes env ran a prep step")
	}
	h.mustLanes(wt, "start")
	if runs() != 2 {
		t.Fatalf("the step did not rerun after its trigger changed (%d runs)", runs())
	}
}

func TestRefusals(t *testing.T) {
	h := newHarness(t)
	h.startDaemon()
	wt := h.worktree("refuse", "feat/refuse")

	cfg := filepath.Join(wt, "lanes.yaml")
	orig := read(cfg)
	if err := os.WriteFile(cfg, []byte(strings.Replace(orig, "schema: 1", "schema: 3", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := h.lanes(wt, "start"); r.code == 0 || !strings.Contains(r.stderr, "brew upgrade valian-ca/tools/lanes") {
		t.Fatalf("newer schema: %s", r)
	}
	if err := os.WriteFile(cfg, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}

	old := t.TempDir()
	if err := os.WriteFile(filepath.Join(old, "process-compose"), []byte("#!/bin/sh\necho 'Version: v1.10.0'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := h.lanesIn(wt, "", []string{"PATH=" + binDir + ":" + old + ":/usr/bin:/bin"}, "start")
	if r.code == 0 || !strings.Contains(r.stderr, "1.10.0") || !strings.Contains(r.stderr, "1.73.0") {
		t.Fatalf("old process-compose: %s", r)
	}

	if err := os.Remove(cfg); err != nil {
		t.Fatal(err)
	}
	if r := h.lanes(wt, "start"); r.code == 0 || !strings.Contains(r.stderr, "no lanes.yaml") {
		t.Fatalf("no lanes.yaml, start: %s", r)
	}
	if r := h.lanes(wt, "env"); r.code != 0 || r.stdout != "" {
		t.Fatalf("no lanes.yaml, env: %s", r)
	}
	if r := h.lanesIn(h.main, payload(wt), nil, "hook", "session-start"); r.code != 0 || r.stdout != "" || r.stderr != "" {
		t.Fatalf("no lanes.yaml, hook: %s", r)
	}
}

func TestMainCheckout(t *testing.T) {
	h := newHarness(t)
	h.startDaemon()
	h.worktree("sibling", "feat/sibling")
	h.mustLanes(h.worktree("listed", "feat/listed"), "env")

	r := h.lanes(h.main, "start")
	if r.code == 0 || !strings.Contains(r.stderr, "run the project's own commands") {
		t.Fatalf("start on the main checkout: %s", r)
	}
	if r := h.lanes(h.main, "env"); r.code != 0 || r.stdout != "" {
		t.Fatalf("env on the main checkout: %s", r)
	}
	envFile := filepath.Join(t.TempDir(), "env")
	for _, hook := range []string{"session-start", "cwd-changed", "session-end"} {
		r := h.lanesIn(h.main, payload(h.main), []string{"CLAUDE_ENV_FILE=" + envFile}, "hook", hook)
		if r.code != 0 || r.stdout != "" || r.stderr != "" {
			t.Fatalf("hook %s on the main checkout: %s", hook, r)
		}
	}
	if _, err := os.Stat(envFile); err == nil {
		t.Fatal("a hook wrote the main checkout's environment")
	}
	for _, l := range h.list().Lanes {
		if l.Worktree == h.main {
			t.Fatal("lanes list shows the main checkout")
		}
	}
}

func TestNoLanesOnPath(t *testing.T) {
	h := newHarness(t)
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct{ Command string } `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(read(filepath.Join(h.main, ".claude", "settings.json"))), &settings); err != nil {
		t.Fatal(err)
	}
	bare := []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir()}
	for event, groups := range settings.Hooks {
		c := exec.Command("/bin/sh", "-c", groups[0].Hooks[0].Command)
		c.Dir, c.Env, c.Stdin = h.main, bare, strings.NewReader(payload(h.main))
		out, err := c.CombinedOutput()
		if err != nil || len(out) != 0 {
			t.Errorf("%s hook without lanes: %v %q", event, err, out)
		}
	}
	direnvShim := `has() { command -v "$1" >/dev/null 2>&1; }; . ./.envrc`
	c := exec.Command("/bin/bash", "-c", direnvShim)
	c.Dir, c.Env = h.main, bare
	if out, err := c.CombinedOutput(); err != nil || len(out) != 0 {
		t.Errorf(".envrc without lanes: %v %q", err, out)
	}
}

func TestGitStatusClean(t *testing.T) {
	h := newHarness(t)
	h.startDaemon()
	wt := h.worktree("clean", "feat/clean")
	h.session(wt)
	h.lanesIn(h.main, payload(wt), nil, "hook", "session-start")
	h.mustLanes(wt, "start")
	h.mustLanes(wt, "up", "extra")
	h.mustLanes(wt, "stop")
	h.git(wt, "switch", "-q", "-c", "feat/clean-2", "origin/main")
	h.mustLanes(wt, "start")
	h.mustLanes(wt, "stop")
	if st := h.git(wt, "status", "--porcelain", "--untracked-files=all"); st != "" {
		t.Fatalf("git status after lanes:\n%s", st)
	}
}

func TestGenerateIdempotent(t *testing.T) {
	h := newHarness(t)
	h.startDaemon()
	wt := h.worktree("gen", "feat/gen")
	h.session(wt)
	h.mustLanes(wt, "start")
	out := filepath.Join(wt, "app.env")
	if want := "WEB_URL=http://127.0.0.1:" + strconv.Itoa(h.port(wt, "web")) + "\n"; !strings.HasPrefix(read(out), want) {
		t.Fatalf("app.env = %q", read(out))
	}
	h.git(wt, "check-ignore", "-q", "app.env")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(out, old, old); err != nil {
		t.Fatal(err)
	}
	h.mustLanes(wt, "start")
	if info, _ := os.Stat(out); !info.ModTime().Equal(old) {
		t.Fatal("an unchanged app.env was rewritten")
	}
}
