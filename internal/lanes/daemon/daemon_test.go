package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/lanes/adapter"
	"github.com/valian-ca/homebrew-tools/internal/lanes/config"
	"github.com/valian-ca/homebrew-tools/internal/lanes/paths"
	"github.com/valian-ca/homebrew-tools/internal/lanes/ports"
	"github.com/valian-ca/homebrew-tools/internal/lanes/procinfo"
	"github.com/valian-ca/homebrew-tools/internal/lanes/registry"
	"github.com/valian-ca/homebrew-tools/internal/lanes/testutil"
	"github.com/valian-ca/homebrew-tools/internal/lanes/tmpdir"
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
	h.s = h.d.get(&stack{Root: root, AdminDir: liveAdminDir(t, t.TempDir()), Tmp: "/tmp/x", Keep: Keep{Sessions: []string{"claude"}}, Running: true})
	return h
}

// liveAdminDir gives dir the gitdir file git keeps in a linked worktree's
// admin dir.
func liveAdminDir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gitdir"), []byte(root+"/.git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
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

func TestSweepSparesTheLauncherOfADesktopSession(t *testing.T) {
	procs := []procinfo.Proc{
		{PID: 100, PPID: 1, PGID: 100, Comm: "Claude", Cwd: "/"},
		{PID: 101, PPID: 100, PGID: 101, Comm: "disclaimer", Argv: []string{"/Applications/Claude.app/Contents/Helpers/disclaimer", "--pgroup"}, Cwd: root},
		{PID: 102, PPID: 101, PGID: 101, Comm: "claude", Cwd: root},
		{PID: 103, PPID: 1, PGID: 103, Comm: "disclaimer", Cwd: root},
	}
	var got []int
	for _, p := range SweepTargets(procs, root, Keep{Sessions: []string{"claude"}}, 999) {
		got = append(got, p.PID)
	}
	if !slices.Equal(got, []int{103}) {
		t.Fatalf("targets = %v, want only the launcher whose session is gone", got)
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

func TestAutomaticSweepYieldsToASessionThatCameBack(t *testing.T) {
	h := newHarness(t)
	var killed []int
	h.d.Kill = func(pid int, _ syscall.Signal) error {
		pid = max(pid, -pid)
		killed = append(killed, pid)
		h.procs.Procs = slices.DeleteFunc(h.procs.Procs, func(p procinfo.Proc) bool { return p.PID == pid })
		return nil
	}
	server := procinfo.Proc{PID: 400, PPID: 1, PGID: 400, Comm: "nc", Cwd: root}
	h.procs.Procs = []procinfo.Proc{session(300), server}
	h.d.sweep(context.Background(), h.s, true)
	if len(killed) != 0 {
		t.Fatalf("an automatic teardown swept %v while a session worked in the worktree", killed)
	}
	h.d.sweep(context.Background(), h.s, false)
	if !slices.Contains(killed, 400) {
		t.Fatal("lanes stop spared the server because a session worked in the worktree")
	}
}

func TestNotifyArmsOnlyRunningStacksAtOrBelowTheRoot(t *testing.T) {
	h := newHarness(t)
	h.d.notify(root + "-other")
	if !h.s.NotifyUntil.IsZero() {
		t.Fatal("a sibling path sharing the root's prefix armed the window")
	}
	h.d.notify(root + "/sub/dir")
	if h.s.NotifyUntil.IsZero() {
		t.Fatal("a SessionEnd from a subdirectory did not arm the window")
	}
	h.d.setRunning(h.s, false)
	h.d.notify(root)
	if !h.s.NotifyUntil.IsZero() {
		t.Fatal("a stopped stack was armed, so a start within the minute would stop at once")
	}
}

func TestReconcileClearsAnAdminDirLeftWithLanesFilesOnly(t *testing.T) {
	h := newHarness(t)
	common := testutil.RealTemp(t)
	t.Setenv("LANES_PORT_RANGE", "43700-43799")
	left := filepath.Join(common, "worktrees", "left")
	live := liveAdminDir(t, filepath.Join(common, "worktrees", "live"))
	for _, admin := range []string{left, live} {
		if _, err := ports.Ensure(ports.Entry{CommonDir: common, AdminDir: admin, Worktree: "/w/" + filepath.Base(admin), Size: 5}); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(paths.LaneDir(admin), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	h.d.reconcile(context.Background())
	if _, ok, _ := ports.Find(left); ok {
		t.Fatal("a worktree whose admin dir lost git's files kept its block")
	}
	if _, err := os.Stat(left); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the admin dir left with lanes' files only was kept: %v", err)
	}
	if _, ok, _ := ports.Find(live); !ok {
		t.Fatal("a live worktree lost its block")
	}
}

func TestReconcileBacksOffAFailedCleanup(t *testing.T) {
	h := newHarness(t)
	common := testutil.RealTemp(t)
	gone := filepath.Join(common, "worktrees", "gone")
	t.Setenv("LANES_PORT_RANGE", "43900-43999")
	if _, err := ports.Ensure(ports.Entry{CommonDir: common, AdminDir: gone, Worktree: "/w/gone", Size: 5}); err != nil {
		t.Fatal(err)
	}
	counter, okFile := filepath.Join(common, "erases"), filepath.Join(common, "ok")
	erase := "echo x >> " + counter + "; test -f " + okFile
	if err := registry.Add(common, registry.Entry{
		Kind: adapter.KindScripts, Adapter: "store", Resource: "r1", Lane: "l1", AdminDir: gone,
		Scripts: &config.Scripts{Name: "store", Erase: erase},
	}); err != nil {
		t.Fatal(err)
	}
	erases := func() int { data, _ := os.ReadFile(counter); return strings.Count(string(data), "x") }
	held := func() bool { _, ok, _ := ports.Find(gone); return ok }
	ctx := context.Background()

	h.d.reconcile(ctx)
	if erases() != 1 || !held() {
		t.Fatalf("after a failed erase: %d erases, block held %v", erases(), held())
	}
	h.advance(time.Minute)
	h.d.reconcile(ctx)
	if erases() != 1 {
		t.Fatal("a failed cleanup was retried before the backoff ended")
	}
	if err := os.WriteFile(okFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h.advance(reconcileRetry)
	h.d.reconcile(ctx)
	if erases() != 2 || held() {
		t.Fatalf("after the backoff: %d erases, block held %v", erases(), held())
	}
	if _, ok := h.d.retryAt[gone]; ok {
		t.Fatal("the retry date outlived a successful cleanup")
	}
}

func TestAdoptKeepsTheSavedGraceAndSkipsDeadStacks(t *testing.T) {
	h := newHarness(t)
	common := testutil.RealTemp(t)
	t.Setenv("LANES_PORT_RANGE", "43800-43899")
	var entries []ports.Entry
	for _, name := range []string{"alive", "dead"} {
		e, err := ports.Ensure(ports.Entry{CommonDir: common, AdminDir: filepath.Join(common, name), Worktree: "/w/" + name, Size: 5})
		if err != nil {
			t.Fatal(err)
		}
		liveAdminDir(t, e.AdminDir)
		entries = append(entries, e)
	}
	alive, dead := entries[0], entries[1]
	h.rt.alive[tmpdir.Path(common, alive.AdminDir)] = true
	grace := time.Unix(1_900_000_000, 0).UTC()
	saved, _ := json.Marshal([]*stack{{AdminDir: alive.AdminDir, GraceUntil: grace}})
	if err := os.WriteFile(paths.Stacks(), saved, 0o600); err != nil {
		t.Fatal(err)
	}

	h.d.adopt(context.Background())
	got, ok := h.d.stacks[alive.AdminDir]
	if !ok || !got.Running || !got.GraceUntil.Equal(grace) {
		t.Fatalf("adopted = %+v, want running with the saved grace", got)
	}
	if _, ok := h.d.stacks[dead.AdminDir]; ok {
		t.Fatal("a stack whose process-compose is gone was adopted")
	}
}
