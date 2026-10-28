package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gofrs/flock"

	"github.com/valian-ca/homebrew-tools/internal/lanes/fsx"
	"github.com/valian-ca/homebrew-tools/internal/lanes/paths"
	"github.com/valian-ca/homebrew-tools/internal/lanes/pc"
	"github.com/valian-ca/homebrew-tools/internal/lanes/ports"
	"github.com/valian-ca/homebrew-tools/internal/lanes/procinfo"
	"github.com/valian-ca/homebrew-tools/internal/lanes/proto"
)

const (
	DefaultGrace        = 5 * time.Minute
	DefaultInterval     = 10 * time.Second
	DefaultStartTimeout = 20 * time.Minute
	// NotifyWindow is how long lanesd watches closely after SessionEnd: the
	// hook fires while the closing claude process is still alive, so its
	// absence only shows a moment later.
	NotifyWindow   = 60 * time.Second
	notifyPoll     = time.Second
	reconcileRetry = 5 * time.Minute
	downTimeout    = 60 * time.Second
	sweepPatience  = 10 * time.Second
	logTailLines   = 40
)

// Runtime is the process-compose side of a stack, swapped for a fake in unit
// tests.
type Runtime interface {
	Alive(ctx context.Context, tmp string) bool
	Down(ctx context.Context, tmp string) error
}

type pcRuntime struct{}

func (pcRuntime) Alive(ctx context.Context, tmp string) bool {
	return pc.NewClient(paths.PCSocket(tmp)).Alive(ctx)
}

func (pcRuntime) Down(ctx context.Context, tmp string) error {
	ctx, cancel := context.WithTimeout(ctx, downTimeout)
	defer cancel()
	return pc.NewClient(paths.PCSocket(tmp)).Down(ctx)
}

type Daemon struct {
	Procs        procinfo.Source
	Runtime      Runtime
	Kill         func(int, syscall.Signal) error
	Now          func() time.Time
	Grace        time.Duration
	Interval     time.Duration
	StartTimeout time.Duration
	Log          *log.Logger
	// Go runs teardowns and reconcile passes off the watch loop, so one slow
	// stack never delays another; unit tests run them inline.
	Go func(func())

	mu          sync.Mutex
	stacks      map[string]*stack
	retryAt     map[string]time.Time
	wake        chan struct{}
	reconciling atomic.Bool
}

type stack struct {
	Root        string    `json:"root"`
	AdminDir    string    `json:"adminDir"`
	CommonDir   string    `json:"commonDir"`
	Tmp         string    `json:"tmp"`
	Keep        Keep      `json:"keep"`
	Running     bool      `json:"running"`
	GraceUntil  time.Time `json:"graceUntil,omitzero"`
	NotifyUntil time.Time `json:"notifyUntil,omitzero"`
	Sessions    int       `json:"-"`

	tearing bool
	op      sync.Mutex
}

func New() *Daemon {
	d := &Daemon{
		Procs:        procinfo.System(),
		Runtime:      pcRuntime{},
		Kill:         syscall.Kill,
		Now:          time.Now,
		Grace:        durationOverride("LANES_GRACE", DefaultGrace),
		Interval:     durationOverride("LANES_SCAN_INTERVAL", DefaultInterval),
		StartTimeout: durationOverride("LANES_START_TIMEOUT", DefaultStartTimeout),
		Log:          log.New(os.Stderr, "lanesd: ", log.LstdFlags),
	}
	d.init()
	return d
}

func (d *Daemon) init() {
	if d.stacks == nil {
		d.stacks = map[string]*stack{}
	}
	if d.wake == nil {
		d.wake = make(chan struct{}, 1)
	}
	if d.retryAt == nil {
		d.retryAt = map[string]time.Time{}
	}
	if d.Go == nil {
		d.Go = func(f func()) { go f() }
	}
}

func durationOverride(name string, def time.Duration) time.Duration {
	if v, ok := paths.TestOverride(name); ok {
		if dur, err := time.ParseDuration(v); err == nil {
			return dur
		}
	}
	return def
}

var ErrAlreadyRunning = errors.New("another lanesd is already running")

// Run serves until ctx ends. It adopts the stacks that survived a restart and
// reconciles what disappeared meanwhile, and never relaunches a stack.
func (d *Daemon) Run(ctx context.Context) error {
	d.init()
	if err := paths.EnsureDir(paths.MustStateDir()); err != nil {
		return err
	}
	lock := flock.New(paths.DaemonLock())
	ok, err := lock.TryLock()
	if err != nil {
		return err
	}
	if !ok {
		return ErrAlreadyRunning
	}
	defer func() { _ = lock.Unlock() }()

	_ = os.Remove(paths.Socket())
	ln, err := net.Listen("unix", paths.Socket())
	if err != nil {
		return err
	}
	defer os.Remove(paths.Socket())
	_ = os.Chmod(paths.Socket(), paths.FileMode)

	d.adopt(ctx)
	d.reconcile(ctx)
	d.Log.Printf("serving on %s", paths.Socket())

	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	go d.watch(ctx)

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go d.serve(ctx, conn)
	}
}

// serve answers one request. A client that hangs up — a Ctrl-C on lanes up —
// cancels the work it asked for.
func (d *Daemon) serve(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	line, err := r.ReadBytes('\n')
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		_, _ = r.ReadByte()
		cancel()
	}()
	var req proto.Request
	var resp proto.Response
	if err := json.Unmarshal(line, &req); err != nil {
		resp = proto.Response{Error: "bad request: " + err.Error()}
	} else {
		resp = d.handle(ctx, req)
	}
	_ = json.NewEncoder(conn).Encode(resp)
}

func (d *Daemon) handle(ctx context.Context, req proto.Request) proto.Response {
	var err error
	resp := proto.Response{OK: true}
	switch req.Op {
	case proto.OpPing:
	case proto.OpStart:
		resp, err = d.start(ctx, req)
	case proto.OpStop:
		err = d.stop(ctx, req.Worktree)
	case proto.OpUp:
		err = d.up(ctx, req, false)
	case proto.OpRestart:
		err = d.up(ctx, req, true)
	case proto.OpNotify:
		d.notify(req.Worktree)
	case proto.OpList:
		resp.Stacks = d.list()
	default:
		err = fmt.Errorf("unknown op %q", req.Op)
	}
	if err != nil {
		return proto.Response{Error: err.Error()}
	}
	resp.OK = true
	return resp
}

func (d *Daemon) find(root string) *stack {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, s := range d.stacks {
		if s.Root == root {
			return s
		}
	}
	return nil
}

func (d *Daemon) get(s *stack) *stack {
	d.mu.Lock()
	defer d.mu.Unlock()
	if cur, ok := d.stacks[s.AdminDir]; ok {
		return cur
	}
	d.stacks[s.AdminDir] = s
	return s
}

func (d *Daemon) notify(root string) {
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	d.mu.Lock()
	for _, s := range d.stacks {
		if s.Root == root || strings.HasPrefix(root, s.Root+"/") {
			s.NotifyUntil = d.Now().Add(NotifyWindow)
		}
	}
	d.mu.Unlock()
	d.poke()
}

func (d *Daemon) poke() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

func (d *Daemon) list() []proto.StackStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]proto.StackStatus, 0, len(d.stacks))
	for _, s := range d.stacks {
		st := proto.StackStatus{AdminDir: s.AdminDir, Running: s.Running, Sessions: s.Sessions}
		if !s.GraceUntil.IsZero() {
			g := s.GraceUntil
			st.GraceUntil = &g
		}
		out = append(out, st)
	}
	return out
}

func (d *Daemon) watch(ctx context.Context) {
	for {
		d.tick(ctx)
		wait := d.Interval
		if d.notifying() {
			wait = notifyPoll
		}
		select {
		case <-ctx.Done():
			return
		case <-d.wake:
		case <-time.After(wait):
		}
	}
}

func (d *Daemon) notifying() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.Now()
	for _, s := range d.stacks {
		if s.Running && now.Before(s.NotifyUntil) {
			return true
		}
	}
	return false
}

// tick applies the stop rules to every stack, then reconciles the worktrees
// that disappeared. A stack found alive again after a failed check is watched
// again.
func (d *Daemon) tick(ctx context.Context) {
	procs, err := d.Procs.Snapshot()
	if err != nil {
		d.Log.Printf("process snapshot: %v", err)
		return
	}
	d.mu.Lock()
	stacks := make([]*stack, 0, len(d.stacks))
	for _, s := range d.stacks {
		stacks = append(stacks, s)
	}
	d.mu.Unlock()

	for _, s := range stacks {
		if _, err := os.Stat(s.AdminDir); err != nil {
			continue
		}
		d.mu.Lock()
		running, tearing := s.Running, s.tearing
		d.mu.Unlock()
		if tearing {
			continue
		}
		alive := d.Runtime.Alive(ctx, s.Tmp)
		switch {
		case running && !alive:
			d.setRunning(s, false)
		case !running && alive && s.op.TryLock():
			d.setRunning(s, true)
			s.op.Unlock()
			d.Log.Printf("%s: its stack is running, watching it again", s.Root)
		}
		if !alive {
			continue
		}
		if reason := d.verdict(s, procs); reason != "" {
			d.teardownLater(ctx, s, reason)
		}
	}
	if d.reconciling.CompareAndSwap(false, true) {
		d.Go(func() {
			defer d.reconciling.Store(false)
			d.reconcile(ctx)
		})
	}
	d.save()
}

// teardownLater stops a stack off the watch loop. It waits for any start or
// stop in progress, then checks the sessions again: one may have come back
// while it waited.
func (d *Daemon) teardownLater(ctx context.Context, s *stack, reason string) {
	d.mu.Lock()
	if s.tearing {
		d.mu.Unlock()
		return
	}
	s.tearing = true
	d.mu.Unlock()
	d.Go(func() {
		defer func() {
			d.mu.Lock()
			s.tearing = false
			d.mu.Unlock()
		}()
		s.op.Lock()
		defer s.op.Unlock()
		if procs, err := d.Procs.Snapshot(); err == nil && sessionsIn(s, procs) > 0 {
			d.verdict(s, procs)
			return
		}
		d.Log.Printf("%s: %s, tearing the stack down", s.Root, reason)
		if err := d.teardownLocked(ctx, s); err != nil {
			d.Log.Printf("%s: teardown: %v", s.Root, err)
		}
	})
}

func sessionsIn(s *stack, procs []procinfo.Proc) int {
	n := 0
	for _, p := range procs {
		if p.Under(s.Root) && p.NamedAs(s.Keep.Sessions) {
			n++
		}
	}
	return n
}

// verdict counts the sessions of a running stack and decides whether it must
// stop now: right after a SessionEnd left no session, or once the grace that
// started when sessions vanished silently has run out.
func (d *Daemon) verdict(s *stack, procs []procinfo.Proc) string {
	n := sessionsIn(s, procs)
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.Now()
	s.Sessions = n
	if n > 0 {
		s.GraceUntil = time.Time{}
		Touchf(s.AdminDir)
		if !now.Before(s.NotifyUntil) {
			s.NotifyUntil = time.Time{}
		}
		return ""
	}
	if now.Before(s.NotifyUntil) {
		return "the last session ended"
	}
	if s.GraceUntil.IsZero() {
		s.GraceUntil = now.Add(d.Grace)
		return ""
	}
	if !now.Before(s.GraceUntil) {
		return "no session came back within the grace period"
	}
	return ""
}

// Touchf is replaced in unit tests that run without a real admin dir.
var Touchf = touch

func touch(adminDir string) {
	p := paths.Activity(adminDir)
	now := time.Now()
	if err := os.Chtimes(p, now, now); err != nil {
		_ = os.WriteFile(p, nil, paths.FileMode)
	}
}

func (d *Daemon) setRunning(s *stack, running bool) {
	d.mu.Lock()
	s.Running = running
	if !running {
		s.GraceUntil, s.NotifyUntil, s.Sessions = time.Time{}, time.Time{}, 0
	}
	d.mu.Unlock()
}

// teardown shuts process-compose down, then sweeps what still works in the
// worktree. The lane's data is left in place.
func (d *Daemon) teardown(ctx context.Context, s *stack) error {
	s.op.Lock()
	defer s.op.Unlock()
	return d.teardownLocked(ctx, s)
}

func (d *Daemon) teardownLocked(ctx context.Context, s *stack) error {
	var downErr error
	if d.Runtime.Alive(ctx, s.Tmp) {
		downErr = d.Runtime.Down(ctx, s.Tmp)
	}
	d.sweep(ctx, s)
	d.setRunning(s, false)
	d.save()
	return downErr
}

func (d *Daemon) sweep(ctx context.Context, s *stack) {
	procs, err := d.Procs.Snapshot()
	if err != nil {
		d.Log.Printf("%s: sweep: %v", s.Root, err)
		return
	}
	targets := SweepTargets(procs, s.Root, s.Keep, os.Getpid())
	if len(targets) == 0 {
		return
	}
	for _, t := range targets {
		d.Log.Printf("%s: stopping %s (pid %d)", s.Root, t.Comm, t.PID)
	}
	send(d.Kill, Signals(procs, targets, syscall.Getpgrp()), syscall.SIGTERM)

	deadline := time.Now().Add(sweepPatience)
	for {
		procs, err = d.Procs.Snapshot()
		if err != nil {
			return
		}
		alive := map[int]procinfo.Proc{}
		for _, p := range procs {
			alive[p.PID] = p
		}
		var left []procinfo.Proc
		for _, t := range targets {
			if p, ok := alive[t.PID]; ok && p.Comm == t.Comm {
				left = append(left, p)
			}
		}
		if len(left) == 0 {
			return
		}
		if !time.Now().Before(deadline) {
			for _, p := range left {
				_ = d.Kill(p.PID, syscall.SIGKILL)
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (d *Daemon) save() {
	d.mu.Lock()
	snapshot := make([]*stack, 0, len(d.stacks))
	for _, s := range d.stacks {
		snapshot = append(snapshot, s)
	}
	data, err := json.Marshal(snapshot)
	d.mu.Unlock()
	if err == nil {
		_ = fsx.WriteAtomic(paths.Stacks(), data, paths.FileMode)
	}
}

// adopt finds the stacks still running after a lanesd restart, through their
// process-compose socket, and takes them back as they are.
func (d *Daemon) adopt(ctx context.Context) {
	var saved []*stack
	if data, err := os.ReadFile(paths.Stacks()); err == nil {
		_ = json.Unmarshal(data, &saved)
	}
	prev := map[string]*stack{}
	for _, s := range saved {
		prev[s.AdminDir] = s
	}
	t, err := ports.Load()
	if err != nil {
		d.Log.Printf("adopt: %v", err)
		return
	}
	for _, e := range t.Entries {
		s := d.stackFor(e)
		if !d.Runtime.Alive(ctx, s.Tmp) {
			continue
		}
		s.Running = true
		if p, ok := prev[e.AdminDir]; ok {
			s.GraceUntil = p.GraceUntil
		}
		d.get(s)
		d.Log.Printf("adopted the running stack of %s", s.Root)
	}
}
