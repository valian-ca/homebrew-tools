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
	return event(cwd, "SessionStart")
}

func event(cwd, name string, kv ...string) string {
	m := map[string]string{"cwd": cwd, "hook_event_name": name, "session_id": "test"}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	data, _ := json.Marshal(m)
	return string(data)
}

func hookContext(t *testing.T, r result, event string) string {
	t.Helper()
	var out struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil {
		t.Fatalf("the hook's stdout is not its JSON output: %v\n%s", err, r)
	}
	if out.HookSpecificOutput.HookEventName != event {
		t.Fatalf("hookEventName = %q, want %q", out.HookSpecificOutput.HookEventName, event)
	}
	return out.HookSpecificOutput.AdditionalContext
}

func hasAll(t *testing.T, text string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(text, w) {
			t.Errorf("the context lacks %q:\n%s", w, text)
		}
	}
}

var laneCommands = []string{"lanes start", "lanes stop", "lanes logs <piece>", "lanes restart <piece>", "lanes test-env -- <command>", "lanes list", "lanes help contract"}

// The hook runs from the main checkout, as Claude Desktop runs it, and still
// exports the lane of the worktree its payload names.
func TestHookExportsTheWorktreeLane(t *testing.T) {
	h := newHarness(t)
	wt := h.worktree("hook", "feat/hook")
	envFile := filepath.Join(t.TempDir(), "claude-env")

	r := h.lanesIn(h.main, payload(wt), []string{"CLAUDE_ENV_FILE=" + envFile, "TMPDIR=/var/folders/claude-imposed"}, "hook", "session-start")
	if r.code != 0 {
		t.Fatalf("hook: %s", r)
	}
	l := h.laneOf(wt)
	hasAll(t, hookContext(t, r, "SessionStart"), "lane "+l.Lane, "Stack: unknown", "brew services start lanes")
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
	hook := func(name, payload string, env ...string) result {
		t.Helper()
		r := h.lanesIn(h.main, payload, env, "hook", name)
		if r.code != 0 {
			t.Fatalf("%s with %s: %s", name, payload, r)
		}
		return r
	}
	tool := func(cwd, name string) string {
		return event(cwd, "PostToolUse", "session_id", "s-enter", "tool_name", name)
	}
	sees := func() string {
		t.Helper()
		out, err := exec.Command("/bin/bash", "-c", `source "$1"; echo "$PORT_WEB $LANE_ID"`, "_", envFile).Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}

	r := hook("session-start", event(h.main, "SessionStart", "session_id", "s-enter"), "CLAUDE_ENV_FILE="+envFile, "TMPDIR=/var/folders/session")
	hasAll(t, hookContext(t, r, "SessionStart"), "this session is on the main checkout", "lanes help contract")
	r = hook("cwd-changed", tool(wt, "EnterWorktree"))
	l := h.laneOf(wt)
	hasAll(t, hookContext(t, r, "PostToolUse"), "lane "+l.Lane, "(worktree "+wt+")", "web="+strconv.Itoa(l.Ports["PORT_WEB"]))
	if got, want := sees(), strconv.Itoa(l.Ports["PORT_WEB"])+" "+l.Lane; got != want {
		t.Fatalf("after EnterWorktree a Bash command sees %q, want %q", got, want)
	}
	r = hook("cwd-changed", tool(h.main, "ExitWorktree"))
	if text := hookContext(t, r, "PostToolUse"); strings.Contains(text, l.Lane) || !strings.Contains(text, "main checkout") {
		t.Fatalf("after ExitWorktree the context is not the main checkout's note:\n%s", text)
	}
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

	r = hook("cwd-changed", event(wt, "CwdChanged", "session_id", "s-enter"))
	if r.stdout != "" {
		t.Fatalf("CwdChanged takes no context, yet the hook wrote: %s", r)
	}
	if got, want := sees(), strconv.Itoa(l.Ports["PORT_WEB"])+" "+l.Lane; got != want {
		t.Fatalf("after a cd a Bash command sees %q, want %q", got, want)
	}
}

func TestContextBlockFollowsTheStack(t *testing.T) {
	h := newHarness(t)
	h.startDaemon()
	wt := h.worktree("block", "feat/block")
	start := func(source string) string {
		t.Helper()
		r := h.lanesIn(h.main, event(wt, "SessionStart", "source", source), nil, "hook", "session-start")
		if r.code != 0 {
			t.Fatalf("session-start (%s): %s", source, r)
		}
		return hookContext(t, r, "SessionStart")
	}

	text := start("startup")
	l := h.laneOf(wt)
	if n := strings.Count(text, "\n") + 1; n > 12 {
		t.Fatalf("the block holds %d lines, 12 at most:\n%s", n, text)
	}
	hasAll(t, text, "lane "+l.Lane, "(worktree "+wt+")", "host 127.0.0.1",
		"Stack: never started. Run `lanes start` before using the ports.",
		"Ports: web="+strconv.Itoa(l.Ports["PORT_WEB"])+" extra="+strconv.Itoa(l.Ports["PORT_EXTRA"]))
	hasAll(t, text, laneCommands...)
	if strings.Contains(text, "test_") {
		t.Errorf("the block names a test_* port:\n%s", text)
	}

	h.session(wt)
	h.mustLanes(wt, "start")
	for _, source := range []string{"compact", "clear", "resume"} {
		if text := start(source); !strings.Contains(text, "Stack: running.") {
			t.Errorf("after %s the block does not say running:\n%s", source, text)
		}
	}
	h.mustLanes(wt, "stop")
	hasAll(t, start("startup"), "Stack: stopped. Run `lanes start` before using the ports.")
}

func TestUnsettledLaneBlock(t *testing.T) {
	h := newHarness(t)
	wt := h.worktree("broken", "feat/broken")
	envFile := filepath.Join(t.TempDir(), "claude-env")
	if r := h.lanesIn(h.main, payload(wt), nil, "hook", "session-start"); r.code != 0 {
		t.Fatalf("session-start: %s", r)
	}
	l := h.laneOf(wt)
	cfg := filepath.Join(wt, "lanes.yaml")
	if err := os.WriteFile(cfg, []byte(read(cfg)+"portz: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := h.lanesIn(h.main, payload(wt), []string{"CLAUDE_ENV_FILE=" + envFile}, "hook", "session-start")
	if r.code != 0 || !strings.Contains(r.stderr, "portz") {
		t.Fatalf("session-start on an invalid lanes.yaml: %s", r)
	}
	text := hookContext(t, r, "SessionStart")
	if n := strings.Count(text, "\n") + 1; n > 12 {
		t.Fatalf("%d lines:\n%s", n, text)
	}
	hasAll(t, text, "(last lane "+l.Lane+")", "Error: "+cfg, "portz", "lanes help contract")
	if strings.Contains(text, "Ports:") {
		t.Errorf("an unsettled block lists ports:\n%s", text)
	}
	if data, _ := os.ReadFile(envFile); len(data) != 0 {
		t.Errorf("an unsettled lane exported:\n%s", data)
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

	r := h.lanesIn(h.main, payload(wt), nil, "hook", "session-start")
	if r.code != 0 || r.stderr == "" {
		t.Fatalf("hook: %s", r)
	}
	hasAll(t, hookContext(t, r, "SessionStart"), "Stack: never started")
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
	if err := os.Remove(filepath.Join(h.main, "lanes.yaml")); err != nil {
		t.Fatal(err)
	}
	if r := h.lanesIn(h.main, payload(h.main), nil, "hook", "session-start"); r.code != 0 || r.stdout != "" || r.stderr != "" {
		t.Fatalf("no lanes.yaml, hook on the main checkout: %s", r)
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
	r = h.lanesIn(h.main, payload(h.main), []string{"CLAUDE_ENV_FILE=" + envFile}, "hook", "session-start")
	if r.code != 0 || r.stderr != "" {
		t.Fatalf("session-start on the main checkout: %s", r)
	}
	note := hookContext(t, r, "SessionStart")
	if lines := strings.Split(note, "\n"); len(lines) != 2 || !strings.Contains(note, "`lanes help contract`") {
		t.Fatalf("the main checkout's note is not two lines naming lanes help contract:\n%s", note)
	}
	for hook, payload := range map[string]string{"cwd-changed": event(h.main, "CwdChanged"), "session-end": event(h.main, "SessionEnd")} {
		r := h.lanesIn(h.main, payload, []string{"CLAUDE_ENV_FILE=" + envFile}, "hook", hook)
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
