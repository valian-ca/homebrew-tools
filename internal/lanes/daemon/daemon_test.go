package daemon

import (
	"context"
	"io"
	"log"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/lanes/procinfo"
	"github.com/valian-ca/homebrew-tools/internal/lanes/testutil"
)

const root = "/w/demo-wt"

type fakeRuntime struct {
	alive map[string]bool
	downs []string
}

func (f *fakeRuntime) Alive(_ context.Context, tmp string) bool { return f.alive[tmp] }

func (f *fakeRuntime) Down(_ context.Context, tmp string) error {
	f.downs = append(f.downs, tmp)
	f.alive[tmp] = false
	return nil
}

type harness struct {
	d     *Daemon
	rt    *fakeRuntime
	procs *procinfo.Fake
	now   time.Time
	s     *stack
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	testutil.StateDir(t)
	Touchf = func(string) {}
	t.Cleanup(func() { Touchf = touch })
	h := &harness{rt: &fakeRuntime{alive: map[string]bool{"/tmp/x": true}}, procs: &procinfo.Fake{}, now: time.Unix(1_800_000_000, 0)}
	h.d = &Daemon{
		Procs:    h.procs,
		Runtime:  h.rt,
		Kill:     func(int, syscall.Signal) error { return nil },
		Now:      func() time.Time { return h.now },
		Grace:    DefaultGrace,
		Interval: DefaultInterval,
		Log:      log.New(io.Discard, "", 0),
		Go:       func(f func()) { f() },
	}
	h.d.init()
	h.s = h.d.get(&stack{Root: root, AdminDir: t.TempDir(), Tmp: "/tmp/x", Keep: Keep{Sessions: []string{"claude"}}, Running: true})
	return h
}

func session(pid int) procinfo.Proc {
	return procinfo.Proc{PID: pid, PPID: 1, PGID: pid, Comm: "claude", Cwd: root + "/sub"}
}

func (h *harness) tick(t *testing.T) {
	t.Helper()
	h.d.tick(context.Background())
}

func (h *harness) advance(d time.Duration) { h.now = h.now.Add(d) }

func TestSessionsKeepTheStackForever(t *testing.T) {
	h := newHarness(t)
	h.procs.Procs = []procinfo.Proc{session(100)}
	for range 50 {
		h.tick(t)
		h.advance(time.Hour)
	}
	if !h.s.Running || len(h.rt.downs) != 0 {
		t.Fatalf("an idle but present session lost its stack: downs=%v", h.rt.downs)
	}
}

func TestKilledSessionGetsAGracePeriod(t *testing.T) {
	h := newHarness(t)
	h.procs.Procs = []procinfo.Proc{session(100)}
	h.tick(t)

	h.procs.Procs = nil
	h.tick(t)
	h.advance(DefaultGrace - time.Second)
	h.tick(t)
	if !h.s.Running {
		t.Fatal("torn down before the grace ended")
	}
	h.advance(time.Second)
	h.tick(t)
	if h.s.Running || len(h.rt.downs) != 1 {
		t.Fatalf("not torn down after the grace: downs=%v", h.rt.downs)
	}
}

func TestSessionReturningWithinGraceKeepsTheStack(t *testing.T) {
	h := newHarness(t)
	h.procs.Procs = nil
	h.tick(t)
	h.advance(4 * time.Minute)
	h.procs.Procs = []procinfo.Proc{session(200)}
	h.tick(t)
	h.advance(10 * time.Minute)
	h.tick(t)
	if !h.s.Running || !h.s.GraceUntil.IsZero() {
		t.Fatalf("a returning session did not cancel the grace: %+v", h.s)
	}
}

func TestSessionEndTearsDownOnceTheLastSessionLeaves(t *testing.T) {
	h := newHarness(t)
	h.procs.Procs = []procinfo.Proc{session(100)}
	h.tick(t)

	h.d.notify(root)
	h.tick(t)
	if !h.s.Running {
		t.Fatal("torn down while the closing session was still alive")
	}
	h.procs.Procs = nil
	h.advance(2 * time.Second)
	h.tick(t)
	if h.s.Running {
		t.Fatal("not torn down right after the last session left")
	}
}

func TestOneOfTwoSessionsLeavingKeepsTheStack(t *testing.T) {
	h := newHarness(t)
	h.procs.Procs = []procinfo.Proc{session(100), session(101)}
	h.tick(t)
	h.d.notify(root)
	h.procs.Procs = []procinfo.Proc{session(101)}
	for range 5 {
		h.advance(time.Second)
		h.tick(t)
	}
	h.advance(NotifyWindow)
	h.tick(t)
	if !h.s.Running || !h.s.NotifyUntil.IsZero() {
		t.Fatalf("the remaining session lost its stack: %+v", h.s)
	}
}

func TestStackThatDiedOnItsOwnIsMarkedStopped(t *testing.T) {
	h := newHarness(t)
	h.rt.alive["/tmp/x"] = false
	h.tick(t)
	if h.s.Running {
		t.Fatal("a dead process-compose still counts as running")
	}
}

func TestSweepTargets(t *testing.T) {
	procs := []procinfo.Proc{
		{PID: 10, PGID: 10, Comm: "nc", Cwd: root},
		{PID: 11, PGID: 11, Comm: "sleep", Argv: []string{"lanes-demo-shared", "3600"}, Cwd: root},
		{PID: 12, PGID: 12, Comm: "zsh", Argv: []string{"-zsh"}, Cwd: root},
		{PID: 13, PGID: 13, Comm: "nvim", Cwd: root + "/src"},
		{PID: 14, PGID: 14, Comm: "nc", Cwd: "/elsewhere"},
		{PID: 15, PGID: 15, Comm: "claude", Cwd: root},
		{PID: 16, PGID: 16, Comm: "java", Argv: []string{"java", "-cp", "x", "org.gradle.launcher.daemon.bootstrap.GradleDaemon"}, Cwd: root},
		{PID: 17, PGID: 17, Comm: "node", Cwd: root + "-other"},
		{PID: 18, PGID: 18, Comm: "lanes", Cwd: root},
		{PID: 19, PGID: 19, Comm: "htop", Cwd: root},
	}
	keep := Keep{Sessions: []string{"claude"}, Shared: []string{"lanes-demo-shared", "GradleDaemon"}, Spare: []string{"htop"}}
	var got []int
	for _, p := range SweepTargets(procs, root, keep, 999) {
		got = append(got, p.PID)
	}
	if !slices.Equal(got, []int{10}) {
		t.Fatalf("targets = %v, want only the hand-launched server", got)
	}
}

func TestSignalsSpareAMixedGroup(t *testing.T) {
	procs := []procinfo.Proc{
		{PID: 20, PGID: 20, Comm: "vite", Cwd: root},
		{PID: 21, PGID: 20, Comm: "esbuild", Cwd: root},
		{PID: 30, PGID: 30, Comm: "zsh", Cwd: root},
		{PID: 31, PGID: 30, Comm: "nc", Cwd: root},
	}
	targets := SweepTargets(procs, root, Keep{}, 999)
	sigs := Signals(procs, targets, 999)
	want := []signal{{pid: 20, group: true}, {pid: 31}}
	if !slices.Equal(sigs, want) {
		t.Fatalf("signals = %+v, want %+v", sigs, want)
	}
}

func TestSweepSparesWhatALanesCommandRuns(t *testing.T) {
	procs := []procinfo.Proc{
		{PID: 40, PPID: 1, PGID: 40, Comm: "lanes", Cwd: root},
		{PID: 41, PPID: 40, PGID: 40, Comm: "process-compose", Cwd: root},
		{PID: 42, PPID: 40, PGID: 40, Comm: "sh", Cwd: root},
		{PID: 43, PPID: 42, PGID: 40, Comm: "pnpm", Cwd: root},
		{PID: 50, PPID: 1, PGID: 50, Comm: "vite", Cwd: root},
	}
	var got []int
	for _, p := range SweepTargets(procs, root, Keep{}, 999) {
		got = append(got, p.PID)
	}
	if !slices.Equal(got, []int{50}) {
		t.Fatalf("targets = %v, want only the stray dev server", got)
	}
}

func TestSweepSparesHelpersButNotWhatShellsRun(t *testing.T) {
	procs := []procinfo.Proc{
		{PID: 60, PPID: 1, PGID: 60, Comm: "claude", Cwd: root},
		{PID: 61, PPID: 60, PGID: 60, Comm: "node", Argv: []string{"node", "mcp-server-linear"}, Cwd: root},
		{PID: 62, PPID: 60, PGID: 62, Comm: "npx", Cwd: root},
		{PID: 63, PPID: 62, PGID: 62, Comm: "node", Cwd: root},
		{PID: 64, PPID: 60, PGID: 64, Comm: "zsh", Cwd: root},
		{PID: 65, PPID: 64, PGID: 64, Comm: "node", Argv: []string{"node", "vite"}, Cwd: root},
		{PID: 70, PPID: 1, PGID: 70, Comm: "Electron", Cwd: "/"},
		{PID: 71, PPID: 70, PGID: 70, Comm: "Code Helper (Plugin)", Cwd: root},
		{PID: 72, PPID: 71, PGID: 70, Comm: "node", Argv: []string{"node", "tsserver.js"}, Cwd: root},
		{PID: 80, PPID: 1, PGID: 80, Comm: "nvim", Cwd: root},
		{PID: 81, PPID: 80, PGID: 80, Comm: "gopls", Cwd: root},
		{PID: 90, PPID: 1, PGID: 90, Comm: "tmux", Cwd: root},
		{PID: 91, PPID: 90, PGID: 91, Comm: "zsh", Cwd: root},
		{PID: 92, PPID: 91, PGID: 92, Comm: "node", Argv: []string{"node", "server.js"}, Cwd: root},
	}
	var got []int
	for _, p := range SweepTargets(procs, root, Keep{Sessions: []string{"claude"}}, 999) {
		got = append(got, p.PID)
	}
	if !slices.Equal(got, []int{65, 92}) {
		t.Fatalf("targets = %v, want only the two servers launched from shells", got)
	}
}

func TestStackAliveAgainAfterAFailedCheckIsWatchedAgain(t *testing.T) {
	h := newHarness(t)
	h.rt.alive["/tmp/x"] = false
	h.tick(t)
	h.rt.alive["/tmp/x"] = true
	h.tick(t)
	if !h.s.Running {
		t.Fatal("a stack alive again is still reported stopped")
	}
	h.advance(DefaultGrace)
	h.tick(t)
	if h.s.Running {
		t.Fatal("the re-watched stack was never torn down")
	}
}

func TestTeardownRechecksSessionsUnderTheLock(t *testing.T) {
	h := newHarness(t)
	h.tick(t)
	h.advance(DefaultGrace)
	var queued []func()
	h.d.Go = func(f func()) { queued = append(queued, f) }
	h.tick(t)
	if !h.s.tearing {
		t.Fatal("no teardown was queued after the grace")
	}
	h.procs.Procs = []procinfo.Proc{session(300)}
	for _, f := range queued {
		f()
	}
	if !h.s.Running || len(h.rt.downs) != 0 {
		t.Fatal("a session that came back while the teardown waited lost its stack")
	}
}
