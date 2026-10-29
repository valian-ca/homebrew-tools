package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/lanes/adapter"
	"github.com/valian-ca/homebrew-tools/internal/lanes/client"
	"github.com/valian-ca/homebrew-tools/internal/lanes/config"
	"github.com/valian-ca/homebrew-tools/internal/lanes/env"
	"github.com/valian-ca/homebrew-tools/internal/lanes/gitx"
	"github.com/valian-ca/homebrew-tools/internal/lanes/lane"
	"github.com/valian-ca/homebrew-tools/internal/lanes/paths"
	"github.com/valian-ca/homebrew-tools/internal/lanes/pc"
	"github.com/valian-ca/homebrew-tools/internal/lanes/ports"
	"github.com/valian-ca/homebrew-tools/internal/lanes/proto"
	"github.com/valian-ca/homebrew-tools/internal/lanes/registry"
	"github.com/valian-ca/homebrew-tools/internal/lanes/tmpdir"
)

var ErrMainCheckout = errors.New("lanes serves git worktrees only — on the main checkout, run the project's own commands")

type Workspace struct {
	WT       gitx.Worktree
	MainRoot string
	Cfg      *config.Config
	Lane     lane.Record
	Block    ports.Entry
	Tmpdir   string
	Vars     env.Vars
}

func Locate(ctx context.Context, dir string) (gitx.Worktree, *config.Config, error) {
	wt, err := gitx.Detect(ctx, dir)
	if err != nil {
		return wt, nil, err
	}
	if wt.IsMain() {
		return wt, nil, ErrMainCheckout
	}
	cfg, err := config.Load(wt.Root)
	return wt, cfg, err
}

func IsQuiet(err error) bool {
	return errors.Is(err, gitx.ErrNotRepo) || errors.Is(err, ErrMainCheckout) || errors.Is(err, config.ErrNotFound)
}

func Settle(ctx context.Context, dir string) (*Workspace, error) {
	wt, cfg, err := Locate(ctx, dir)
	if err != nil {
		return nil, err
	}
	mainRoot, err := gitx.MainRoot(ctx, wt.Root)
	if err != nil {
		return nil, err
	}
	block, err := ports.Ensure(ports.Entry{
		CommonDir: wt.CommonDir,
		AdminDir:  wt.AdminDir,
		Worktree:  wt.Root,
		Project:   filepath.Base(mainRoot),
		Size:      cfg.Ports.Block,
	})
	if err != nil {
		return nil, err
	}
	w := &Workspace{WT: wt, MainRoot: mainRoot, Cfg: cfg, Block: block, Tmpdir: tmpdir.Path(wt.CommonDir, wt.AdminDir)}
	if err := tmpdir.Ensure(w.Tmpdir); err != nil {
		return nil, err
	}
	if err := registry.Add(wt.CommonDir, registry.Entry{
		Kind:      registry.KindTmpdir,
		Adapter:   "core",
		Resource:  w.Tmpdir,
		AdminDir:  wt.AdminDir,
		Worktree:  wt.Root,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		return nil, err
	}
	rec, _, err := lane.Check(ctx, wt, w.retire)
	if err != nil {
		return nil, err
	}
	w.setLane(rec)
	Touch(wt.AdminDir)
	return w, nil
}

// Inspect reads a worktree's lane as it stands, without settling identity or
// allocating anything: the daemon and `lanes list` look, they never decide.
func Inspect(ctx context.Context, root string) (*Workspace, error) {
	wt, cfg, err := Locate(ctx, root)
	if err != nil {
		return nil, err
	}
	block, ok, err := ports.Find(wt.AdminDir)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%s has no port block yet — run lanes start there", wt.Root)
	}
	rec, err := lane.Load(wt.AdminDir)
	if err != nil {
		return nil, err
	}
	w := &Workspace{WT: wt, Cfg: cfg, Block: block, Tmpdir: tmpdir.Path(wt.CommonDir, wt.AdminDir)}
	if rec != nil {
		w.setLane(*rec)
	} else {
		w.setLane(lane.Record{})
	}
	return w, nil
}

func (w *Workspace) setLane(rec lane.Record) {
	w.Lane = rec
	w.Vars = env.Build(env.Lane{
		ID:         rec.ID,
		Worktree:   w.WT.Root,
		Tmpdir:     w.Tmpdir,
		BlockStart: w.Block.Start,
		BlockSize:  w.Block.Size,
		PortNames:  w.Cfg.Ports.Names,
	})
}

func (w *Workspace) Environ(base []string) []string { return w.Vars.Environ(base) }

func (w *Workspace) Adapters(environ []string) []adapter.Adapter {
	return adapter.FromConfig(w.Cfg, adapter.Env{Dir: w.WT.Root, Worktree: w.WT.Root, Environ: environ})
}

func Touch(adminDir string) {
	p := paths.Activity(adminDir)
	now := time.Now()
	if err := os.Chtimes(p, now, now); err != nil {
		_ = os.WriteFile(p, nil, paths.FileMode)
	}
}

func LastActivity(adminDir string) time.Time {
	if info, err := os.Stat(paths.Activity(adminDir)); err == nil {
		return info.ModTime()
	}
	return time.Time{}
}

func (w *Workspace) retire(ctx context.Context, old lane.Record) error {
	if err := StopStack(ctx, w.WT.Root, w.Tmpdir); err != nil {
		return fmt.Errorf("stop the stack of lane %s: %w", old.ID, err)
	}
	entries, err := registry.Load(w.WT.CommonDir)
	if err != nil {
		return err
	}
	prev := *w
	prev.setLane(old)
	environ := prev.Environ(os.Environ())
	for _, en := range entries {
		if en.AdminDir != w.WT.AdminDir || en.Lane != old.ID || en.Kind == registry.KindTmpdir {
			continue
		}
		if err := eraseFrom(ctx, en, old.ID, environ, w.WT.Root, w.MainRoot); err != nil {
			return err
		}
		if err := registry.Remove(w.WT.CommonDir, en); err != nil {
			return err
		}
	}
	return nil
}

// eraseFrom runs the erase from the worktree, then from the main checkout: the
// branch that replaced the old lane may not carry the old lane's scripts.
func eraseFrom(ctx context.Context, en registry.Entry, laneID string, environ []string, root, mainRoot string) error {
	var err error
	for _, dir := range []string{root, mainRoot} {
		a, ferr := adapter.FromEntry(en, adapter.Env{Dir: dir, Worktree: root, Environ: environ})
		if ferr != nil {
			return ferr
		}
		if err = a.Erase(ctx, laneID, en.Resource); err == nil {
			return nil
		}
	}
	return err
}

func StopStack(ctx context.Context, root, tmp string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	_, err := client.Call(ctx, proto.Request{Op: proto.OpStop, Worktree: root})
	if !errors.Is(err, client.ErrDaemonDown) {
		return err
	}
	c := pc.NewClient(paths.PCSocket(tmp))
	if !c.Alive(ctx) {
		return nil
	}
	return c.Down(ctx)
}

// CreateResources runs each adapter's Create once per lane, registering what
// it made before marking it done, so a crash leaves at worst an orphan that
// reconcile finds through List and Owner.
func (w *Workspace) CreateResources(ctx context.Context, environ []string) error {
	for _, a := range w.Adapters(environ) {
		if w.Lane.HasCreated(a.Name()) {
			continue
		}
		if err := a.Preflight(ctx); err != nil {
			return err
		}
		names, err := a.Create(ctx, w.Lane.ID)
		if err != nil {
			return err
		}
		for _, n := range names {
			en := a.Entry(w.Lane.ID, n)
			en.AdminDir = w.WT.AdminDir
			if err := registry.Add(w.WT.CommonDir, en); err != nil {
				return err
			}
		}
		if err := lane.MarkCreated(w.WT, w.Lane.ID, a.Name()); err != nil {
			return err
		}
		w.Lane.Created = append(w.Lane.Created, a.Name())
	}
	return nil
}
