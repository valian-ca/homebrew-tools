package adapter

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/lanes/config"
	"github.com/valian-ca/homebrew-tools/internal/lanes/env"
	"github.com/valian-ca/homebrew-tools/internal/lanes/firebase"
	"github.com/valian-ca/homebrew-tools/internal/lanes/pc"
	"github.com/valian-ca/homebrew-tools/internal/lanes/procinfo"
	"github.com/valian-ca/homebrew-tools/internal/lanes/registry"
)

const (
	KindFirebase  = "firebase"
	FirebasePiece = "firebase"
)

type Firebase struct {
	Spec      config.Firebase
	PortNames []string
	Env       Env
}

func (f *Firebase) Kind() string { return KindFirebase }

func (f *Firebase) Name() string { return FirebasePiece }

// Preflight also ends what a killed firebase piece left: its orphans still
// hold the block's ports, which the start checks before the piece can run.
func (f *Firebase) Preflight(context.Context) error {
	b, err := firebase.LoadBase(f.Env.Worktree, &f.Spec)
	if err != nil {
		return err
	}
	if missing := firebase.Missing(firebase.NewPlan(b, &f.Spec, firebase.Stack).RequiredPorts(), f.PortNames); len(missing) > 0 {
		return firebase.MissingError(missing)
	}
	if err := firebase.CheckSocket(lookup(f.Env.Environ, "TMPDIR")); err != nil {
		return err
	}
	if _, err := firebase.Binary(f.Env.Worktree, &f.Spec); err != nil {
		return err
	}
	firebase.SweepOrphans(procinfo.System(), f.Env.Worktree, f.Spec.ExportTimeout.Std()+firebase.ExportGrace, io.Discard)
	return nil
}

func (f *Firebase) Create(context.Context, string) ([]string, error) {
	return []string{filepath.Join(f.Env.Worktree, f.Spec.Data)}, nil
}

// Erase stays inside the worktree the entry names, whatever its resource
// says: the registry outlives the lanes.yaml that described it.
func (f *Firebase) Erase(_ context.Context, _, resource string) error {
	root := filepath.Clean(f.Env.Worktree)
	if root == "" || !strings.HasPrefix(filepath.Clean(resource), root+"/") {
		return fmt.Errorf("%s adapter: %s is outside the worktree %s — not erased", KindFirebase, resource, root)
	}
	for _, p := range []string{resource, resource + ".next", resource + ".old"} {
		if err := os.RemoveAll(p); err != nil {
			return err
		}
	}
	return nil
}

func (f *Firebase) List(context.Context) ([]string, error) { return nil, nil }

func (f *Firebase) Owner(context.Context, string) (string, error) { return "", nil }

// Processes leaves depends_on and environment to the project, whose
// process-compose.yaml is merged after the fragment.
func (f *Firebase) Processes() map[string]pc.Process {
	grace := f.Spec.ExportTimeout.Std() + firebase.ExportGrace
	probes := int((f.Spec.FunctionsTimeout.Std() + 5*time.Minute) / time.Second)
	return map[string]pc.Process{FirebasePiece: {
		"command":     env.Quote(LanesExecutable()) + " firebase run",
		"working_dir": f.Env.Worktree,
		"readiness_probe": map[string]any{
			"exec":              map[string]any{"command": firebase.ReadyProbe},
			"period_seconds":    1,
			"failure_threshold": probes,
		},
		"shutdown": map[string]any{
			"signal":          15,
			"parent_only":     true,
			"timeout_seconds": int(grace / time.Second),
		},
	}}
}

func (f *Firebase) Entry(lane, resource string) registry.Entry {
	return registry.Entry{
		Kind:      KindFirebase,
		Adapter:   FirebasePiece,
		Resource:  resource,
		Lane:      lane,
		Worktree:  f.Env.Worktree,
		CreatedAt: time.Now().UTC(),
	}
}

// LanesExecutable is the lanes beside the running binary: the fragment is
// written by lanesd, and the formula installs both side by side.
func LanesExecutable() string {
	self, err := os.Executable()
	if err != nil {
		return "lanes"
	}
	if filepath.Base(self) == "lanes" {
		return self
	}
	sibling := filepath.Join(filepath.Dir(self), "lanes")
	if _, err := os.Stat(sibling); err == nil {
		return sibling
	}
	return "lanes"
}

func lookup(environ []string, name string) string {
	if environ == nil {
		environ = os.Environ()
	}
	v := ""
	for _, kv := range environ {
		if k, val, ok := strings.Cut(kv, "="); ok && k == name {
			v = val
		}
	}
	return v
}
