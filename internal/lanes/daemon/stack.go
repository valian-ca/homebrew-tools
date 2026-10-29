package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/lanes/adapter"
	"github.com/valian-ca/homebrew-tools/internal/lanes/config"
	"github.com/valian-ca/homebrew-tools/internal/lanes/fsx"
	"github.com/valian-ca/homebrew-tools/internal/lanes/gitx"
	"github.com/valian-ca/homebrew-tools/internal/lanes/lane"
	"github.com/valian-ca/homebrew-tools/internal/lanes/paths"
	"github.com/valian-ca/homebrew-tools/internal/lanes/pc"
	"github.com/valian-ca/homebrew-tools/internal/lanes/ports"
	"github.com/valian-ca/homebrew-tools/internal/lanes/prep"
	"github.com/valian-ca/homebrew-tools/internal/lanes/proto"
	"github.com/valian-ca/homebrew-tools/internal/lanes/registry"
	"github.com/valian-ca/homebrew-tools/internal/lanes/tmpdir"
	"github.com/valian-ca/homebrew-tools/internal/lanes/workspace"
)

func (d *Daemon) stackFor(e ports.Entry) *stack {
	d.mu.Lock()
	s, ok := d.stacks[e.AdminDir]
	d.mu.Unlock()
	if ok {
		return s
	}
	s = &stack{Root: e.Worktree, AdminDir: e.AdminDir, CommonDir: e.CommonDir, Tmp: tmpdir.Path(e.CommonDir, e.AdminDir)}
	if cfg, err := config.Load(e.Worktree); err == nil {
		s.Keep = Keep{Sessions: cfg.Sessions, Shared: cfg.Shared, Spare: cfg.Spare}
	} else {
		s.Keep = Keep{Sessions: config.DefaultSessions}
	}
	return s
}

func (d *Daemon) start(ctx context.Context, req proto.Request) (proto.Response, error) {
	w, err := workspace.Inspect(ctx, req.Worktree)
	if err != nil {
		return proto.Response{}, err
	}
	s := d.get(&stack{
		Root:      w.WT.Root,
		AdminDir:  w.WT.AdminDir,
		CommonDir: w.WT.CommonDir,
		Tmp:       w.Tmpdir,
	})
	s.op.Lock()
	defer s.op.Unlock()

	d.mu.Lock()
	s.Root, s.CommonDir, s.Tmp = w.WT.Root, w.WT.CommonDir, w.Tmpdir
	s.Keep = Keep{Sessions: w.Cfg.Sessions, Shared: w.Cfg.Shared, Spare: w.Cfg.Spare}
	s.NotifyUntil = time.Time{}
	d.mu.Unlock()

	resp := proto.Response{OK: true}
	if d.Runtime.Alive(ctx, s.Tmp) {
		resp.AlreadyRunning = true
	} else {
		if err := d.launch(ctx, w, req); err != nil {
			return proto.Response{}, err
		}
	}

	procs, err := d.Procs.Snapshot()
	if err != nil {
		return proto.Response{}, err
	}
	sessions := d.sessionsIn(s, procs)
	d.mu.Lock()
	s.Running = true
	s.Sessions = sessions
	if s.Sessions == 0 {
		resp.NoSession = true
		if s.GraceUntil.IsZero() {
			s.GraceUntil = d.Now().Add(d.Grace)
		}
	}
	d.mu.Unlock()
	d.save()
	return resp, nil
}

func (d *Daemon) launch(ctx context.Context, w *workspace.Workspace, req proto.Request) error {
	environ := w.Environ(req.Environ)
	for _, a := range w.Adapters(environ) {
		if err := a.Preflight(ctx); err != nil {
			return err
		}
	}
	d.clearOrphans(ctx, w.WT.Root, Keep{Sessions: w.Cfg.Sessions, Shared: w.Cfg.Shared, Spare: w.Cfg.Spare}, w.Block)
	if err := d.checkPorts(w.Block); err != nil {
		return err
	}

	projectFile := filepath.Join(w.WT.Root, pc.ProjectFile)
	project, err := pc.ProjectProcesses(projectFile)
	if err != nil {
		return err
	}
	adapterProcs := map[string]pc.Process{}
	for _, a := range w.Adapters(environ) {
		for name, p := range a.Processes() {
			adapterProcs[name] = p
		}
	}
	fragment, err := pc.Fragment(project, adapterProcs, paths.Logs(w.Tmpdir))
	if err != nil {
		return err
	}
	if _, err := fsx.WriteIfChanged(filepath.Join(paths.FragmentDir(w.WT.Root), ".gitignore"), []byte("*\n"), 0o644); err != nil {
		return err
	}
	if _, err := fsx.WriteIfChanged(paths.Fragment(w.WT.Root), fragment, 0o644); err != nil {
		return err
	}
	if err := tmpdir.Ensure(w.Tmpdir); err != nil {
		return err
	}

	upCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := pc.Up(upCtx, pc.UpSpec{
		Binary:   req.Binary,
		Worktree: w.WT.Root,
		Files:    []string{paths.Fragment(w.WT.Root), projectFile},
		Socket:   paths.PCSocket(w.Tmpdir),
		LogFile:  paths.PCLog(w.Tmpdir),
		Environ:  environ,
	}); err != nil {
		return err
	}
	if err := d.awaitReady(ctx, w.Tmpdir, "", d.StartTimeout); err != nil {
		if downErr := d.Runtime.Down(context.WithoutCancel(ctx), w.Tmpdir); downErr != nil {
			d.Log.Printf("%s: down after a failed start: %v", w.WT.Root, downErr)
		}
		return err
	}
	return nil
}

// clearOrphans ends what a crashed stack left listening on the block, so the
// start only refuses a port something alive still holds. It runs only while
// no process-compose answers for the lane: a live stack's pieces would read
// as orphans of a daemonized process-compose.
func (d *Daemon) clearOrphans(ctx context.Context, root string, keep Keep, block ports.Entry) {
	var holders []int
	for p := block.Start; p <= block.End(); p++ {
		if !ports.Busy(p) {
			continue
		}
		if pid, ok := d.Procs.ListeningPID(p); ok {
			holders = append(holders, pid)
		}
	}
	if len(holders) == 0 {
		return
	}
	procs, err := d.Procs.Snapshot()
	if err != nil {
		return
	}
	targets := OrphanHolders(procs, root, keep, os.Getpid(), holders)
	for _, t := range targets {
		d.Log.Printf("%s: stopping %s (pid %d), left by an earlier stack", root, t.Comm, t.PID)
	}
	if len(targets) > 0 {
		End(ctx, d.Procs, d.Kill, procs, targets, sweepPatience)
	}
}

// A port busy with no visible listener (a closing connection, another user's
// process) is left for the piece itself to report.
func (d *Daemon) checkPorts(block ports.Entry) error {
	for p := block.Start; p <= block.End(); p++ {
		if !ports.Busy(p) {
			continue
		}
		pid, ok := d.Procs.ListeningPID(p)
		if !ok {
			continue
		}
		name := "an unknown process"
		if procs, err := d.Procs.Snapshot(); err == nil {
			if proc, ok := procinfoByPID(procs, pid); ok {
				name = proc
			}
		}
		return fmt.Errorf("port %d is held by %s (pid %d) — stop it, then run lanes start again", p, name, pid)
	}
	return nil
}

func (d *Daemon) awaitReady(ctx context.Context, tmp, only string, timeout time.Duration) error {
	c := pc.NewClient(paths.PCSocket(tmp))
	deadline := time.Now().Add(timeout)
	var pending []string
	for {
		states, err := c.States(ctx)
		if err == nil {
			if only != "" {
				states = slices.DeleteFunc(states, func(s pc.State) bool { return s.Name != only })
			}
			ready, fail := pc.Evaluate(states)
			if fail != nil {
				return pieceError(tmp, states, fail)
			}
			if ready {
				return nil
			}
			pending = pending[:0]
			for _, s := range states {
				if s.Status != pc.StatusDisabled && s.Status != pc.StatusCompleted {
					pending = append(pending, s.Name)
				}
			}
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("the stack was not ready after %s (still waiting on: %s) — see lanes logs <piece>", timeout, strings.Join(pending, ", "))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func pieceError(tmp string, states []pc.State, fail *pc.Failure) error {
	logPath := paths.PieceLog(tmp, fail.Piece)
	tail := ""
	if data, err := os.ReadFile(logPath); err == nil {
		tail = prep.Tail(string(data), logTailLines)
	}
	reason := fail.Reason
	for _, s := range states {
		if s.Name == fail.Piece && pc.KilledForMemory(s, tail) {
			reason = "was killed for lack of memory"
		}
	}
	return fmt.Errorf("%s %s\n--- last %d lines of %s ---\n%s", fail.Piece, reason, logTailLines, logPath, tail)
}

// stop finishes even when its caller gives up waiting: a half-done teardown
// would leave pieces running with nothing watching them.
func (d *Daemon) stop(ctx context.Context, root string) error {
	ctx = context.WithoutCancel(ctx)
	wt, err := gitx.Detect(ctx, root)
	if err != nil {
		return err
	}
	s := d.get(d.stackFor(ports.Entry{Worktree: wt.Root, AdminDir: wt.AdminDir, CommonDir: wt.CommonDir}))
	return d.teardown(ctx, s)
}

func (d *Daemon) up(ctx context.Context, req proto.Request, restart bool) error {
	s := d.find(req.Worktree)
	if s == nil || !d.Runtime.Alive(ctx, s.Tmp) {
		return errors.New("the stack is not running — run lanes start first")
	}
	s.op.Lock()
	defer s.op.Unlock()
	c := pc.NewClient(paths.PCSocket(s.Tmp))
	states, err := c.States(ctx)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(states, func(st pc.State) bool { return st.Name == req.Proc }) {
		return fmt.Errorf("the stack has no process named %q", req.Proc)
	}
	if restart {
		err = c.Restart(ctx, req.Proc)
	} else {
		err = c.Start(ctx, req.Proc)
	}
	if err != nil {
		return err
	}
	time.Sleep(500 * time.Millisecond)
	return d.awaitReady(ctx, s.Tmp, req.Proc, d.StartTimeout)
}

// A worktree whose cleanup failed keeps its port block and is retried later.
func (d *Daemon) reconcile(ctx context.Context) {
	t, err := ports.Load()
	if err != nil {
		d.Log.Printf("reconcile: %v", err)
		return
	}
	for _, e := range t.Entries {
		if gone, _ := gitx.Gone(e.AdminDir); !gone {
			continue
		}
		d.mu.Lock()
		retry := d.retryAt[e.AdminDir]
		d.mu.Unlock()
		if d.Now().Before(retry) {
			continue
		}
		d.Log.Printf("%s is gone, reconciling", e.Worktree)
		err := d.clear(ctx, t, e)
		d.mu.Lock()
		if err != nil {
			d.retryAt[e.AdminDir] = d.Now().Add(reconcileRetry)
		} else {
			delete(d.retryAt, e.AdminDir)
			delete(d.stacks, e.AdminDir)
		}
		d.mu.Unlock()
		if err != nil {
			d.Log.Printf("%s: reconcile, retrying in %s: %v", e.Worktree, reconcileRetry, err)
		}
	}
}

func (d *Daemon) clear(ctx context.Context, t ports.Table, e ports.Entry) error {
	s := d.stackFor(e)
	if err := d.teardown(ctx, s); err != nil && d.Runtime.Alive(ctx, s.Tmp) {
		return fmt.Errorf("its stack is still running: %w", err)
	}
	var failed []error
	if _, err := os.Stat(e.CommonDir); err == nil {
		mainRoot, _ := gitx.MainRoot(ctx, e.CommonDir)
		failed = d.eraseRegistered(ctx, e, mainRoot)
		d.eraseOrphans(ctx, t, e, mainRoot)
	}
	if err := os.RemoveAll(tmpdir.Path(e.CommonDir, e.AdminDir)); err != nil {
		failed = append(failed, err)
	}
	if err := os.RemoveAll(paths.LaneDir(e.AdminDir)); err != nil {
		failed = append(failed, err)
	}
	if len(failed) > 0 {
		return errors.Join(failed...)
	}
	// Claude Code leaves the admin dir behind once git's files are gone; it is
	// removed only when nothing else is left in it.
	_ = os.Remove(e.AdminDir)
	return ports.Remove(e.AdminDir)
}

// eraseRegistered erases every resource the gone worktree registered, going on
// past a failure so one broken adapter does not keep the others' data.
func (d *Daemon) eraseRegistered(ctx context.Context, e ports.Entry, mainRoot string) []error {
	entries, err := registry.Load(e.CommonDir)
	if err != nil {
		return []error{err}
	}
	var failed []error
	for _, en := range entries {
		if en.AdminDir != e.AdminDir {
			continue
		}
		if err := erase(ctx, en, e, mainRoot); err != nil {
			failed = append(failed, err)
			continue
		}
		if err := registry.Remove(e.CommonDir, en); err != nil {
			failed = append(failed, err)
		}
	}
	return failed
}

func erase(ctx context.Context, en registry.Entry, e ports.Entry, mainRoot string) error {
	if en.Kind == registry.KindTmpdir {
		return os.RemoveAll(en.Resource)
	}
	a, err := adapter.FromEntry(en, adapter.Env{Dir: mainRoot, Worktree: e.Worktree})
	if err != nil {
		return err
	}
	return a.Erase(ctx, en.Lane, en.Resource)
}

// eraseOrphans catches resources created but never registered — a crash
// between an adapter's create and the registry write — by asking each
// adapter of the main checkout's lanes.yaml for its inventory and owners. An
// adapter lists the whole machine, so every repo's live lanes are kept: two
// clones of one project share a database server.
func (d *Daemon) eraseOrphans(ctx context.Context, t ports.Table, gone ports.Entry, mainRoot string) {
	if mainRoot == "" {
		return
	}
	cfg, err := config.Load(mainRoot)
	if err != nil {
		return
	}
	live := map[string]bool{}
	for _, e := range t.Entries {
		gone, err := gitx.Gone(e.AdminDir)
		if gone {
			continue
		}
		rec, lerr := lane.Load(e.AdminDir)
		if err != nil || lerr != nil {
			d.Log.Printf("%s: lane unreadable, skipping the orphan pass: %v", e.Worktree, errors.Join(err, lerr))
			return
		}
		if rec != nil {
			live[rec.ID] = true
		}
	}
	registered := map[string]bool{}
	if entries, err := registry.Load(gone.CommonDir); err == nil {
		for _, en := range entries {
			registered[en.Adapter+"\x00"+en.Resource] = true
		}
	}
	for _, a := range adapter.FromConfig(cfg, adapter.Env{Dir: mainRoot, Worktree: gone.Worktree}) {
		names, err := a.List(ctx)
		if err != nil {
			d.Log.Printf("%s: list: %v", a.Name(), err)
			continue
		}
		for _, n := range names {
			if registered[a.Name()+"\x00"+n] {
				continue
			}
			owner, err := a.Owner(ctx, n)
			if err != nil || owner == "" || live[owner] {
				continue
			}
			d.Log.Printf("%s: erasing orphan %s of lane %s", a.Name(), n, owner)
			if err := a.Erase(ctx, owner, n); err != nil {
				d.Log.Printf("%s: erase %s: %v", a.Name(), n, err)
			}
		}
	}
}
