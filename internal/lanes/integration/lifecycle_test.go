//go:build integration && darwin

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/lanes/ports"
)

func sessionEnd(h *harness, wt string) result {
	return h.lanesIn(h.main, `{"cwd":"`+wt+`","hook_event_name":"SessionEnd"}`, nil, "hook", "session-end")
}

func TestSessionEndTeardown(t *testing.T) {
	h := newHarness(t, grace(time.Hour))
	h.startDaemon()
	wt := h.worktree("end", "feat/end")
	s := h.session(wt)
	h.mustLanes(wt, "start")
	web, token := h.port(wt, "web"), h.token(wt)

	began := time.Now()
	if r := sessionEnd(h, wt); r.code != 0 || time.Since(began) > time.Second {
		t.Fatalf("session-end took %s: %s", time.Since(began), r)
	}
	_ = s.Process.Kill()
	h.eventually("ports freed after the last session ended", 90*time.Second, func() bool { return !listening(web) })

	h.session(wt)
	h.mustLanes(wt, "start")
	if got := h.token(wt); got != token || token == "" {
		t.Fatalf("data after restart = %q, want %q", got, token)
	}
}

func TestSessionEndFromTheMainCheckoutFindsTheWorktree(t *testing.T) {
	h := newHarness(t, grace(time.Hour))
	h.startDaemon()
	wt := h.worktree("left", "feat/left")
	s := h.session(wt)
	if r := h.lanesIn(h.main, `{"cwd":"`+wt+`","session_id":"s-1","hook_event_name":"SessionStart"}`, nil, "hook", "session-start"); r.code != 0 {
		t.Fatalf("session-start: %s", r)
	}
	h.mustLanes(wt, "start")
	web := h.port(wt, "web")

	_ = s.Process.Kill()
	if r := h.lanesIn(h.main, `{"cwd":"`+h.main+`","session_id":"s-1","hook_event_name":"SessionEnd"}`, nil, "hook", "session-end"); r.code != 0 {
		t.Fatalf("session-end: %s", r)
	}
	h.eventually("ports freed after a SessionEnd naming the main checkout", 90*time.Second, func() bool { return !listening(web) })
}

func TestTwoSessionsOneLeaves(t *testing.T) {
	h := newHarness(t, grace(time.Hour))
	h.startDaemon()
	wt := h.worktree("two", "feat/two")
	first := h.session(wt)
	h.session(wt)
	h.mustLanes(wt, "start")
	web := h.port(wt, "web")

	sessionEnd(h, wt)
	_ = first.Process.Kill()
	time.Sleep(4 * time.Second)
	if !listening(web) || h.laneOf(wt).Session != "active" {
		t.Fatalf("the stack stopped while a session remained: %+v", h.laneOf(wt))
	}
}

func TestKilledSessionGrace(t *testing.T) {
	h := newHarness(t, grace(4*time.Second))
	h.startDaemon()
	wt := h.worktree("killed", "feat/killed")
	s := h.session(wt)
	h.mustLanes(wt, "start")
	web := h.port(wt, "web")

	_ = s.Process.Signal(syscall.SIGKILL)
	time.Sleep(2 * time.Second)
	if !listening(web) {
		t.Fatal("torn down before the grace ended")
	}
	h.eventually("ports freed after the grace", 20*time.Second, func() bool { return !listening(web) })
}

func TestSessionReturnsWithinGrace(t *testing.T) {
	h := newHarness(t, grace(5*time.Second))
	h.startDaemon()
	wt := h.worktree("back", "feat/back")
	s := h.session(wt)
	h.mustLanes(wt, "start")
	web := h.port(wt, "web")

	_ = s.Process.Signal(syscall.SIGKILL)
	time.Sleep(2 * time.Second)
	h.session(wt)
	time.Sleep(8 * time.Second)
	if !listening(web) {
		t.Fatal("the stack stopped although a session came back within the grace")
	}
}

func TestDetachedHeadKeepsLane(t *testing.T) {
	h := newHarness(t)
	h.startDaemon()
	wt := h.worktree("archive", "feat/archive")
	h.session(wt)
	h.mustLanes(wt, "start")
	before := h.laneOf(wt)
	token := h.token(wt)
	h.mustLanes(wt, "stop")

	h.git(wt, "checkout", "-q", "--detach")
	h.mustLanes(wt, "start")
	after := h.laneOf(wt)
	if after.Lane != before.Lane || after.Block != before.Block || h.token(wt) != token {
		t.Fatalf("archive changed the lane: %+v → %+v", before, after)
	}
}

func TestRenameThenRebaseKeepsLane(t *testing.T) {
	h := newHarness(t)
	h.startDaemon()
	wt := h.worktree("rename", "feat/rename")
	h.session(wt)
	h.run(wt, "sh", "-c", "echo work > work.txt && git add work.txt && git commit -q -m work")
	h.mustLanes(wt, "start")
	before, token := h.laneOf(wt).Lane, h.token(wt)

	h.git(wt, "branch", "-m", "feat/renamed")
	h.run(h.main, "sh", "-c", "echo up > upstream.txt && git add upstream.txt && git commit -q -m up && git push -q origin main")
	h.git(wt, "fetch", "-q", "origin")
	h.git(wt, "rebase", "-q", "origin/main")
	h.mustLanes(wt, "start")
	if h.laneOf(wt).Lane != before || h.token(wt) != token {
		t.Fatalf("rename + rebase: lane %s → %s", before, h.laneOf(wt).Lane)
	}
}

func TestUnrelatedBranchNewLane(t *testing.T) {
	h := newHarness(t)
	h.startDaemon()
	wt := h.worktree("reuse", "feat/first")
	h.session(wt)
	h.mustLanes(wt, "start")
	old := h.laneOf(wt)
	web := old.Ports["PORT_WEB"]

	h.git(wt, "switch", "-q", "-c", "feat/second", "origin/main")
	h.mustLanes(wt, "env")
	if h.storeHas(old.Lane) {
		t.Fatal("the first lanes command kept the old lane's resource")
	}
	if listening(web) {
		t.Fatal("the old lane's stack still runs after a new lane was born")
	}
	h.mustLanes(wt, "start")
	l := h.laneOf(wt)
	if l.Lane == old.Lane || l.Block != old.Block || h.token(wt) == "" {
		t.Fatalf("new lane %+v, old %+v", l, old)
	}
}

func TestResetToMainNewLane(t *testing.T) {
	h := newHarness(t)
	h.startDaemon()
	wt := h.worktree("merged", "feat/merged")
	h.session(wt)
	h.run(wt, "sh", "-c", "echo f > feature.txt && git add feature.txt && git commit -q -m feature")
	h.mustLanes(wt, "start")
	old := h.laneOf(wt).Lane
	h.mustLanes(wt, "stop")

	h.run(h.main, "sh", "-c", "git merge -q --squash feat/merged && git commit -q -m squash && git push -q origin main")
	h.git(wt, "fetch", "-q", "origin")
	h.git(wt, "reset", "-q", "--hard", "origin/main")
	h.mustLanes(wt, "start")
	if h.laneOf(wt).Lane == old {
		t.Fatal("a branch reset onto main after its merge kept its lane")
	}
}

func TestResetToMainAfterMergeCommitNewLane(t *testing.T) {
	h := newHarness(t)
	h.startDaemon()
	wt := h.worktree("merged", "feat/merged")
	h.session(wt)
	h.run(wt, "sh", "-c", "echo f > feature.txt && git add feature.txt && git commit -q -m feature")
	h.mustLanes(wt, "start")
	old := h.laneOf(wt).Lane
	h.mustLanes(wt, "stop")

	h.run(h.main, "sh", "-c", "git merge -q --no-ff -m 'Merge feat/merged' feat/merged && git push -q origin main")
	h.git(wt, "fetch", "-q", "origin")
	if r := h.lanesIn(h.main, payload(wt), nil, "hook", "session-start"); r.code != 0 {
		t.Fatalf("hook between fetch and reset: %s", r)
	}
	if h.laneOf(wt).Lane != old {
		t.Fatal("a fetched merge changed the lane before any reset")
	}
	h.git(wt, "reset", "-q", "--hard", "origin/main")
	h.mustLanes(wt, "start")
	if h.laneOf(wt).Lane == old || h.storeHas(old) {
		t.Fatal("a branch reset onto main after a merge commit kept its lane")
	}
}

func TestStopKeepsData(t *testing.T) {
	h := newHarness(t)
	h.startDaemon()
	wt := h.worktree("stopme", "feat/stopme")
	h.session(wt)
	h.mustLanes(wt, "start")
	web, token := h.port(wt, "web"), h.token(wt)

	h.mustLanes(h.main, "stop", wt)
	if listening(web) || h.laneOf(wt).State != "stopped" {
		t.Fatal("lanes stop <worktree> left the stack running")
	}
	h.mustLanes(wt, "start")
	h.mustLanes(wt, "stop")
	if listening(web) {
		t.Fatal("lanes stop in the worktree left the stack running")
	}
	h.mustLanes(wt, "start")
	if h.token(wt) != token {
		t.Fatal("stop lost the lane's data")
	}
}

func TestSweep(t *testing.T) {
	h := newHarness(t, grace(time.Hour))
	h.startDaemon()
	wt := h.worktree("sweep", "feat/sweep")
	h.session(wt)
	h.mustLanes(wt, "start")

	server := h.spawn(wt, "/usr/bin/nc", "-lk", "127.0.0.1", "0")
	shared := h.spawn(wt, "lanes-demo-shared", "600")
	shell := h.spawn(wt, "/bin/zsh", "-c", "while :; do sleep 1; done")
	editor := h.spawn(wt, "nvim", "600")
	outside := h.spawn(h.main, "/usr/bin/nc", "-lk", "127.0.0.1", "0")
	group := exec.Command("/usr/bin/nc", "-lk", "127.0.0.1", "0")
	group.Dir, group.Env, group.SysProcAttr = wt, h.env, &syscall.SysProcAttr{Setpgid: true}
	if err := group.Start(); err != nil {
		t.Fatal(err)
	}
	h.track(group)

	h.mustLanes(wt, "stop")
	h.eventually("the hand-launched server to stop", 15*time.Second, func() bool { return !alive(server) })
	h.eventually("the server in its own process group to stop", 15*time.Second, func() bool { return !alive(group) })
	if !strings.Contains(h.daemonLog(), "stopping nc") {
		t.Errorf("the sweep did not log what it stopped:\n%s", h.daemonLog())
	}
	for what, c := range map[string]*exec.Cmd{"shared process": shared, "shell": shell, "editor": editor, "process outside the worktree": outside} {
		if !alive(c) {
			t.Errorf("the %s was stopped by the sweep", what)
		}
	}
}

func TestListJSON(t *testing.T) {
	h := newHarness(t, grace(time.Hour))
	h.startDaemon()
	running := h.worktree("running", "feat/running")
	cfg := filepath.Join(running, "lanes.yaml")
	if err := os.WriteFile(cfg, []byte(read(cfg)+"ram_alert: 1KB\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.session(running)
	h.mustLanes(running, "start")
	stopped := h.worktree("stopped", "feat/stopped")
	h.mustLanes(stopped, "start")
	h.mustLanes(stopped, "stop")

	r := h.list()
	if !r.DaemonRunning || len(r.Lanes) != 2 {
		t.Fatalf("report = %+v", r)
	}
	run, stop := h.laneOf(running), h.laneOf(stopped)
	if run.State != "running" || run.Session != "active" || run.LastActivity == nil || run.Logs == "" || len(run.Ports) != 2 {
		t.Fatalf("running lane = %+v", run)
	}
	var web int64
	for _, p := range run.Pieces {
		if p.Name == "web" {
			web = p.RAM
		}
	}
	if web == 0 || !run.RAMAlert {
		t.Fatalf("RAM not measured or not flagged: %+v", run)
	}
	if stop.State != "stopped" || stop.Worktree != stopped {
		t.Fatalf("stopped lane = %+v", stop)
	}
	time.Sleep(3 * time.Second)
	if h.laneOf(running).State != "running" {
		t.Fatal("a lane above its RAM threshold was stopped")
	}
}

func TestWorktreeRemovedReconcile(t *testing.T) {
	h := newHarness(t, grace(time.Hour))
	h.startDaemon()
	wt := h.worktree("gone", "feat/gone")
	h.mustLanes(wt, "start")
	l := h.laneOf(wt)
	web := l.Ports["PORT_WEB"]

	removed := time.Now()
	h.git(h.main, "worktree", "remove", "--force", wt)
	h.eventually("the stack's processes to stop", 90*time.Second, func() bool { return !listening(web) })
	h.eventually("the registered resource and TMPDIR to go", 2*time.Minute, func() bool {
		_, err := os.Stat(l.Tmpdir)
		return !h.storeHas(l.Lane) && os.IsNotExist(err)
	})
	t.Logf("cleared %s after removal", time.Since(removed).Round(time.Second))
	h.eventually("the block to be free", 10*time.Second, func() bool {
		t, _ := ports.Load()
		for _, e := range t.Entries {
			if e.Worktree == wt {
				return false
			}
		}
		return true
	})
}

// Claude Code removes a worktree by deleting git's files from its admin dir,
// and keeps whatever else lives there.
func TestWorktreeRemovedByClaudeReconcile(t *testing.T) {
	h := newHarness(t, grace(time.Hour))
	h.startDaemon()
	wt := h.worktree("claude-gone", "feat/claude-gone")
	h.mustLanes(wt, "start")
	l := h.laneOf(wt)
	web, admin := l.Ports["PORT_WEB"], h.adminDir(wt)

	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(admin)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "lanes" {
			if err := os.RemoveAll(filepath.Join(admin, e.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}
	h.eventually("the stack's processes to stop", 90*time.Second, func() bool { return !listening(web) })
	h.eventually("the resource, the TMPDIR and the admin dir to go", 2*time.Minute, func() bool {
		_, terr := os.Stat(l.Tmpdir)
		_, aerr := os.Stat(admin)
		return !h.storeHas(l.Lane) && os.IsNotExist(terr) && os.IsNotExist(aerr)
	})
	if lanes := h.list().Lanes; len(lanes) != 0 {
		t.Fatalf("lanes list still shows %+v", lanes)
	}
}

func TestDaemonStartupReconcilesWithoutRelaunch(t *testing.T) {
	h := newHarness(t, grace(time.Hour))
	h.startDaemon()
	keep := h.worktree("keep", "feat/keep")
	drop := h.worktree("drop", "feat/drop")
	h.mustLanes(keep, "start")
	h.mustLanes(drop, "start")
	keepToken, dropLane := h.token(keep), h.laneOf(drop)

	h.stopDaemon()
	h.mustLanes(keep, "stop")
	h.mustLanes(drop, "stop")
	h.git(h.main, "worktree", "remove", "--force", drop)

	h.startDaemon()
	h.eventually("leftovers of the deleted worktree to be erased", 30*time.Second, func() bool {
		_, err := os.Stat(dropLane.Tmpdir)
		return !h.storeHas(dropLane.Lane) && os.IsNotExist(err)
	})
	if h.laneOf(keep).State != "stopped" {
		t.Fatal("lanesd relaunched a stack at startup")
	}
	h.session(keep)
	h.mustLanes(keep, "start")
	if h.token(keep) != keepToken {
		t.Fatal("the surviving worktree lost its data")
	}
}

func TestDaemonRestartAdoptsStacks(t *testing.T) {
	h := newHarness(t, grace(time.Hour))
	h.startDaemon()
	a := h.worktree("a", "feat/a")
	b := h.worktree("b", "feat/b")
	h.session(a)
	h.session(b)
	h.mustLanes(a, "start")
	h.mustLanes(b, "start")
	pa, pb := h.piecePID(a, "web"), h.piecePID(b, "web")

	h.stopDaemon()
	down := h.list()
	if down.DaemonRunning || h.laneOf(a).State != "running" || h.laneOf(a).Session != "unknown" {
		t.Fatalf("lanes list with lanesd down: %+v", h.laneOf(a))
	}
	h.startDaemon()
	if h.piecePID(a, "web") != pa || h.piecePID(b, "web") != pb {
		t.Fatal("a lanesd restart restarted a stack")
	}
	for _, wt := range []string{a, b} {
		if l := h.laneOf(wt); l.State != "running" || l.Session != "active" {
			t.Fatalf("after restart: %+v", l)
		}
	}
	if out := h.mustLanes(a, "start"); !strings.Contains(out, "already running") {
		t.Fatalf("start after adoption: %s", out)
	}
}

func TestReconcileKeepsOtherReposLanesAndErasesOrphans(t *testing.T) {
	h := newHarness(t, grace(time.Hour))
	h.startDaemon()
	other := h.repo("other")
	gone := h.worktree("gone", "feat/gone")
	kept := h.worktreeIn(other, "kept", "feat/kept")
	h.mustLanes(gone, "start")
	h.mustLanes(kept, "start")
	keptLane, keptToken := h.laneOf(kept).Lane, h.token(kept)
	orphan := filepath.Join(h.store, "feat_crashed_0a0b0c")
	if err := os.MkdirAll(orphan, 0o755); err != nil {
		t.Fatal(err)
	}

	goneLane := h.laneOf(gone).Lane
	h.git(h.main, "worktree", "remove", "--force", gone)
	h.eventually("the gone worktree's resource and the orphan to be erased", time.Minute, func() bool {
		_, err := os.Stat(orphan)
		return !h.storeHas(goneLane) && os.IsNotExist(err)
	})
	if !h.storeHas(keptLane) || h.token(kept) != keptToken {
		t.Fatal("reconciling one repo erased a live lane of another")
	}
	if h.laneOf(kept).State != "running" {
		t.Fatal("reconciling one repo stopped another repo's stack")
	}
}

func TestNewLaneOnABranchWithoutTheScripts(t *testing.T) {
	h := newHarness(t)
	h.startDaemon()
	wt := h.worktree("noscript", "feat/with-store")
	h.mustLanes(wt, "start")
	old := h.laneOf(wt).Lane
	h.mustLanes(wt, "stop")

	h.git(wt, "switch", "-q", "-c", "feat/no-store", "origin/main")
	h.run(wt, "sh", "-c", "git rm -q tool/lane-store && git commit -q -m 'drop the store'")
	if r := h.lanes(wt, "env"); r.code != 0 {
		t.Fatalf("lanes env on a branch without the old lane's script: %s", r)
	}
	if h.storeHas(old) {
		t.Fatal("the old lane's resource survived its retirement")
	}
}
