//go:build integration && darwin

package integration

import (
	"bytes"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/lanes/ports"
)

func TestStartFreshWorktree(t *testing.T) {
	h := newHarness(t)
	h.startDaemon()
	other := h.worktree("other", "feat/other")
	h.session(other)
	h.mustLanes(other, "start")

	wt := h.worktree("fresh", "feat/fresh")
	h.session(wt)
	out := h.mustLanes(wt, "start")
	if !strings.Contains(out, "is ready") {
		t.Fatalf("start output: %s", out)
	}
	l, o := h.laneOf(wt), h.laneOf(other)
	if l.State != "running" || l.Block.Start < ports.RangeLow || l.Block.End > ports.RangeHigh {
		t.Fatalf("lane = %+v", l)
	}
	if l.Block.Start <= o.Block.End && o.Block.Start <= l.Block.End {
		t.Fatalf("blocks overlap: %+v and %+v", l.Block, o.Block)
	}
	if len(l.Tmpdir) > 40 {
		t.Fatalf("TMPDIR %q is %d characters", l.Tmpdir, len(l.Tmpdir))
	}
	if !listening(l.Ports["PORT_WEB"]) {
		t.Fatal("web is not listening on PORT_WEB")
	}
}

func TestStartWithoutDaemon(t *testing.T) {
	h := newHarness(t)
	wt := h.worktree("nodaemon", "feat/nodaemon")
	r := h.lanes(wt, "start")
	if r.code == 0 || !strings.Contains(r.stderr, "brew services start lanes") {
		t.Fatalf("start without lanesd: %s", r)
	}
	if _, err := os.Stat(filepath.Join(wt, ".lanes")); err == nil {
		t.Fatal("start wrote the fragment without lanesd")
	}
	if _, err := os.Stat(filepath.Join(h.adminDir(wt), "lanes", "lane.json")); err == nil {
		t.Fatal("start settled a lane without lanesd")
	}
}

func TestStartForeignPortHolder(t *testing.T) {
	h := newHarness(t)
	h.startDaemon()
	wt := h.worktree("foreign", "feat/foreign")
	h.mustLanes(wt, "env")
	start := h.laneOf(wt).Block.Start

	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(start+1))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	r := h.lanes(wt, "start")
	want := "port " + strconv.Itoa(start+1) + " is held by integration.test (pid " + strconv.Itoa(os.Getpid()) + ")"
	if r.code == 0 || !strings.Contains(r.stderr, want) {
		t.Fatalf("want %q, got %s", want, r)
	}
	if h.laneOf(wt).State != "stopped" {
		t.Fatal("a stack started beside a foreign port holder")
	}
}

func TestStartAfterACrashEndsTheOrphans(t *testing.T) {
	h := newHarness(t)
	h.startDaemon()
	wt := h.worktree("crash", "feat/crash")
	h.session(wt)
	h.mustLanes(wt, "start")
	left := h.crashStack(wt)

	h.mustLanes(wt, "start")
	if alive := stillAlive(left); len(alive) > 0 {
		t.Fatalf("lanes start left the crashed stack's pieces: %v", alive)
	}
	if !strings.Contains(h.daemonLog(), "left by an earlier stack") {
		t.Errorf("the start did not log what it ended:\n%s", h.daemonLog())
	}
	// A server a live shell runs is still refused, by name.
	h.mustLanes(wt, "stop")
	port := strconv.Itoa(h.laneOf(wt).Block.Start + 1)
	shell := h.spawn(wt, "/bin/zsh", "-c", "/usr/bin/nc -lk 127.0.0.1 "+port+"; true")
	h.eventually("the shell's server to listen", 10*time.Second, func() bool { return listening(h.laneOf(wt).Block.Start + 1) })
	if r := h.lanes(wt, "start"); r.code == 0 || !strings.Contains(r.stderr, "port "+port+" is held by nc") {
		t.Fatalf("start beside a live shell's server: %s", r)
	}
	if !alive(shell) {
		t.Fatal("the start ended a server a live shell owns")
	}
}

func TestStartPieceDies(t *testing.T) {
	h := newHarness(t)
	h.startDaemon()
	wt := h.worktree("dies", "feat/dies")
	pcFile := filepath.Join(wt, "process-compose.yaml")
	if err := os.WriteFile(pcFile, []byte(strings.ReplaceAll(read(pcFile), "    disabled: true\n", "    disabled: false\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	r := h.lanes(wt, "start")
	if r.code == 0 || !strings.Contains(r.stderr, "crashy exited with code 3") || !strings.Contains(r.stderr, "crashy: cannot open the database") {
		t.Fatalf("start with a dying piece: %s", r)
	}
	h.eventually("the failed stack to be down", 30*time.Second, func() bool { return h.laneOf(wt).State == "stopped" })
}

func TestStartIdempotent(t *testing.T) {
	h := newHarness(t)
	h.startDaemon()
	wt := h.worktree("twice", "feat/twice")
	h.session(wt)
	h.mustLanes(wt, "start")
	before := h.piecePID(wt, "web")

	h.session(wt)
	out := h.mustLanes(wt, "start")
	if !strings.Contains(out, "already running") || h.piecePID(wt, "web") != before || before == 0 {
		t.Fatalf("second start: %s (web pid %d, then %d)", out, before, h.piecePID(wt, "web"))
	}
}

func TestStartWithoutSession(t *testing.T) {
	h := newHarness(t)
	h.startDaemon()
	wt := h.worktree("alone", "feat/alone")
	out := h.mustLanes(wt, "start")
	if !strings.Contains(out, "no Claude session in this worktree") {
		t.Fatalf("start output: %s", out)
	}
	web := h.port(wt, "web")
	if h.laneOf(wt).Session != "grace" {
		t.Fatalf("session = %q, want grace", h.laneOf(wt).Session)
	}
	h.eventually("ports freed after the grace", 15*time.Second, func() bool { return !listening(web) })
}

func TestUpAndRestart(t *testing.T) {
	h := newHarness(t)
	h.startDaemon()
	wt := h.worktree("updown", "feat/updown")
	h.session(wt)
	h.mustLanes(wt, "start")
	extra := h.port(wt, "extra")
	if listening(extra) {
		t.Fatal("the on-demand process runs before lanes up")
	}
	h.mustLanes(wt, "up", "extra")
	if !listening(extra) {
		t.Fatal("extra is not listening on PORT_EXTRA after lanes up")
	}
	web, extraPID := h.piecePID(wt, "web"), h.piecePID(wt, "extra")
	h.mustLanes(wt, "restart", "extra")
	if h.piecePID(wt, "web") != web || h.piecePID(wt, "extra") == extraPID {
		t.Fatalf("restart extra: web %d→%d, extra %d→%d", web, h.piecePID(wt, "web"), extraPID, h.piecePID(wt, "extra"))
	}
}

func TestLogs(t *testing.T) {
	h := newHarness(t)
	h.startDaemon()
	wt := h.worktree("logs", "feat/logs")
	h.session(wt)
	h.mustLanes(wt, "start")
	h.lanes(wt, "up", "crashy")
	h.mustLanes(wt, "stop")

	out := h.mustLanes(wt, "logs", "crashy")
	if strings.TrimSpace(out) != "crashy: cannot open the database" {
		t.Fatalf("logs after stop = %q", out)
	}
	if r := h.lanes(wt, "logs", "nope"); r.code == 0 || !strings.Contains(r.stderr, "crashy") {
		t.Fatalf("logs of an unknown piece: %s", r)
	}

	var buf lockedBuffer
	follow := exec.Command(filepath.Join(binDir, "lanes"), "logs", "crashy", "-f")
	follow.Dir, follow.Env, follow.Stdout = wt, h.env, &buf
	if err := follow.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = follow.Process.Kill() }()
	h.eventually("lanes logs -f prints the tail", 5*time.Second, func() bool { return strings.Contains(buf.String(), "cannot open") })

	f, err := os.OpenFile(filepath.Join(h.tmpdir(wt), "logs", "crashy.log"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("a new line\n"); err != nil {
		t.Fatal(err)
	}
	h.eventually("lanes logs -f follows new lines", 5*time.Second, func() bool { return strings.Contains(buf.String(), "a new line") })
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
