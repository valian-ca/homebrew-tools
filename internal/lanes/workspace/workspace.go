package workspace

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
	"github.com/valian-ca/homebrew-tools/internal/lanes/client"
	"github.com/valian-ca/homebrew-tools/internal/lanes/config"
	"github.com/valian-ca/homebrew-tools/internal/lanes/env"
	"github.com/valian-ca/homebrew-tools/internal/lanes/firebase"
	"github.com/valian-ca/homebrew-tools/internal/lanes/gitx"
	"github.com/valian-ca/homebrew-tools/internal/lanes/lane"
	"github.com/valian-ca/homebrew-tools/internal/lanes/mysql"
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
	// EnvErr is the failed render of lanes.yaml's env: entries; Vars then
	// holds every variable but theirs.
	EnvErr error
	// Deferred are the erases of a retired lane that could not reach their
	// resource: the lane changed anyway, and lanesd retries them.
	Deferred []error
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

// Inspect never settles: settling can retire the lane, which stops the very stack its callers serve.
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
	l := env.Lane{
		ID:         rec.ID,
		Worktree:   w.WT.Root,
		Tmpdir:     w.Tmpdir,
		BlockStart: w.Block.Start,
		BlockSize:  w.Block.Size,
		PortNames:  w.Cfg.Ports.Names,
		Host:       rec.Host,
		Extra:      append(w.firebaseVars(firebase.Stack), w.mysqlVars(rec.ID)...),
	}
	w.Vars = env.Build(l)
	project, err := renderEnv(w.Cfg.Env, w.Vars)
	w.EnvErr = err
	if err != nil || len(project) == 0 {
		return
	}
	l.Extra = append(l.Extra, project...)
	w.Vars = env.Build(l)
}

func (w *Workspace) mysqlVars(laneID string) env.Vars {
	m := w.Cfg.MySQL()
	if m == nil {
		return nil
	}
	return mysql.Vars(*m, laneID)
}

func renderEnv(entries config.EnvVars, base env.Vars) (env.Vars, error) {
	seen := base.Map()
	out := make(env.Vars, 0, len(entries))
	for _, e := range entries {
		t, err := e.Parse()
		if err != nil {
			return nil, fmt.Errorf("env.%s: %w", e.Name, err)
		}
		var b strings.Builder
		if err := t.Execute(&b, seen); err != nil {
			return nil, fmt.Errorf("env.%s: %w", e.Name, err)
		}
		seen[e.Name] = b.String()
		out = append(out, env.Var{Name: e.Name, Value: b.String()})
	}
	return out, nil
}

// firebaseVars is empty when the base config is unreadable or a port is
// missing: the generation names the cause.
func (w *Workspace) firebaseVars(s firebase.Series) env.Vars {
	fb := w.Cfg.Firebase()
	if fb == nil {
		return nil
	}
	b, err := firebase.LoadBase(w.WT.Root, fb)
	if err != nil {
		return nil
	}
	plan := firebase.NewPlan(b, fb, s)
	ports := firebase.Ports(w.Cfg.Ports.Names, w.Block.Start)
	if len(firebase.Missing(plan.RequiredPorts(), w.Cfg.Ports.Names)) > 0 {
		return nil
	}
	return firebase.Vars(plan, ports, firebase.ConfigPath(w.WT.Root, fb, s))
}

func (w *Workspace) TestVars() env.Vars {
	over := env.Vars{{Name: "TMPDIR", Value: filepath.Join(w.Tmpdir, "test")}}
	fb := w.Cfg.Firebase()
	if fb == nil {
		return w.testProjectVars(w.Vars.With(over))
	}
	if b, err := firebase.LoadBase(w.WT.Root, fb); err == nil {
		test := firebase.NewPlan(b, fb, firebase.Test)
		stack := firebase.NewPlan(b, fb, firebase.Stack)
		ports := firebase.Ports(w.Cfg.Ports.Names, w.Block.Start)
		over = append(over, firebase.PortVars(test, ports)...)
		over = append(over, w.firebaseVars(firebase.Test)...)
		set := make(map[string]bool, len(over))
		for _, v := range over {
			set[v.Name] = true
		}
		for _, v := range append(firebase.PortVars(stack, ports), w.firebaseVars(firebase.Stack)...) {
			if !set[v.Name] {
				set[v.Name] = true
				over = append(over, env.Var{Name: v.Name, Value: ""})
			}
		}
	}
	return w.testProjectVars(w.Vars.With(over))
}

// testProjectVars renders the env: entries again over the test series, so a
// value composed from a test-series variable follows it.
func (w *Workspace) testProjectVars(v env.Vars) env.Vars {
	if w.EnvErr != nil || len(w.Cfg.Env) == 0 {
		return v
	}
	project, err := renderEnv(w.Cfg.Env, v)
	if err != nil {
		return v
	}
	return v.With(project)
}

func (w *Workspace) SetHost(host string) {
	rec := w.Lane
	rec.Host = host
	w.setLane(rec)
}

func (w *Workspace) Environ(base []string) []string { return w.Vars.Environ(base) }

func (w *Workspace) Adapters(environ []string) []adapter.Adapter {
	return adapter.FromConfig(w.Cfg, adapter.Env{
		Dir:         w.WT.Root,
		Worktree:    w.WT.Root,
		Environ:     environ,
		WithoutLane: w.EnvironWithoutLane(),
		MainRoot:    w.MainRoot,
	})
}

func (w *Workspace) EnvironWithoutLane() []string {
	listed := strings.Fields(os.Getenv("LANES_VARS"))
	drop := map[string]bool{}
	for _, n := range slices.Concat(env.Fixed, w.Vars.Names(), listed) {
		drop[n] = true
	}
	if !slices.Contains(listed, "TMPDIR") {
		delete(drop, "TMPDIR")
	}
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if !drop[k] && !strings.HasPrefix(k, "LANES_MYSQL_") {
			out = append(out, kv)
		}
	}
	return out
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
			if errors.Is(err, adapter.ErrDeferred) {
				w.Deferred = append(w.Deferred, fmt.Errorf("lane %s: %w", old.ID, err))
				continue
			}
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
		if err = a.Erase(ctx, laneID, en.Resource); err == nil || errors.Is(err, adapter.ErrDeferred) {
			return err
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

// CreateResources registers each resource before marking its adapter done, so
// a crash leaves at worst an orphan that reconcile finds through List and Owner.
func (w *Workspace) CreateResources(ctx context.Context, environ []string) error {
	for _, a := range w.Adapters(environ) {
		if e, ok := a.(adapter.Ensurer); ok {
			if err := e.Ensure(ctx, w.Lane.ID, func(n string) error { return registry.Refresh(w.WT.CommonDir, w.entry(a, n)) }); err != nil {
				return err
			}
			if err := w.markCreated(a); err != nil {
				return err
			}
			continue
		}
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
			if err := registry.Add(w.WT.CommonDir, w.entry(a, n)); err != nil {
				return err
			}
		}
		if err := w.markCreated(a); err != nil {
			return err
		}
	}
	return nil
}

func (w *Workspace) entry(a adapter.Adapter, resource string) registry.Entry {
	en := a.Entry(w.Lane.ID, resource)
	en.AdminDir = w.WT.AdminDir
	return en
}

func (w *Workspace) markCreated(a adapter.Adapter) error {
	if w.Lane.HasCreated(a.Name()) {
		return nil
	}
	if err := lane.MarkCreated(w.WT, w.Lane.ID, a.Name()); err != nil {
		return err
	}
	w.Lane.Created = append(w.Lane.Created, a.Name())
	return nil
}
