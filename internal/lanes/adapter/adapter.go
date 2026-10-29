package adapter

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/lanes/config"
	"github.com/valian-ca/homebrew-tools/internal/lanes/pc"
	"github.com/valian-ca/homebrew-tools/internal/lanes/registry"
	"github.com/valian-ca/homebrew-tools/internal/lanes/shellx"
)

type Adapter interface {
	Kind() string
	Name() string
	Preflight(ctx context.Context) error
	// Create runs once per lane, at the lane's first start.
	Create(ctx context.Context, lane string) ([]string, error)
	Erase(ctx context.Context, lane, resource string) error
	// List returns every resource of this adapter on the machine, whichever
	// lane owns it, so orphans that never reached the registry can be found.
	List(ctx context.Context) ([]string, error)
	// Owner returns "" for a resource that is not a lane's.
	Owner(ctx context.Context, resource string) (string, error)
	Processes() map[string]pc.Process
	Entry(lane, resource string) registry.Entry
}

type Env struct {
	Dir      string
	Worktree string
	Environ  []string
}

func FromConfig(cfg *config.Config, e Env) []Adapter {
	var out []Adapter
	for _, r := range cfg.Resources {
		switch {
		case r.Scripts != nil:
			out = append(out, &Scripts{Spec: *r.Scripts, Env: e})
		case r.Firebase != nil:
			out = append(out, &Firebase{Spec: *r.Firebase, PortNames: cfg.Ports.Names, Env: e})
		}
	}
	return out
}

// FromEntry rebuilds the adapter that created a registry entry, from the
// entry alone, so a resource stays erasable once its worktree is gone.
func FromEntry(en registry.Entry, e Env) (Adapter, error) {
	switch {
	case en.Kind == KindScripts && en.Scripts != nil:
		return &Scripts{Spec: *en.Scripts, Env: e}, nil
	case en.Kind == KindFirebase:
		return &Firebase{Env: e}, nil
	}
	return nil, fmt.Errorf("registry entry %s/%s: unknown adapter kind %q", en.Adapter, en.Resource, en.Kind)
}

const KindScripts = "scripts"

var ScriptTimeout = 2 * time.Minute

type Scripts struct {
	Spec config.Scripts
	Env  Env
}

func (s *Scripts) Kind() string { return KindScripts }

func (s *Scripts) Name() string { return s.Spec.Name }

func (s *Scripts) Preflight(context.Context) error { return nil }

func (s *Scripts) Processes() map[string]pc.Process { return nil }

func (s *Scripts) Create(ctx context.Context, lane string) ([]string, error) {
	out, err := s.run(ctx, "create", s.Spec.Create, lane, "")
	if err != nil {
		return nil, err
	}
	return lines(out), nil
}

func (s *Scripts) Erase(ctx context.Context, lane, resource string) error {
	_, err := s.run(ctx, "erase", s.Spec.Erase, lane, resource)
	return err
}

func (s *Scripts) List(ctx context.Context) ([]string, error) {
	out, err := s.run(ctx, "list", s.Spec.List, "", "")
	if err != nil {
		return nil, err
	}
	return lines(out), nil
}

func (s *Scripts) Owner(ctx context.Context, resource string) (string, error) {
	out, err := s.run(ctx, "owner", s.Spec.Owner, "", resource)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (s *Scripts) Entry(lane, resource string) registry.Entry {
	spec := s.Spec
	return registry.Entry{
		Kind:      KindScripts,
		Adapter:   s.Spec.Name,
		Resource:  resource,
		Lane:      lane,
		Worktree:  s.Env.Worktree,
		CreatedAt: time.Now().UTC(),
		Scripts:   &spec,
	}
}

func (s *Scripts) run(ctx context.Context, verb, command, lane, resource string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, ScriptTimeout)
	defer cancel()
	c := shellx.Command(ctx, command)
	c.Dir = s.Env.Dir
	environ := s.Env.Environ
	if environ == nil {
		environ = os.Environ()
	}
	c.Env = append(append([]string(nil), environ...),
		"LANES_LANE="+lane,
		"LANES_WORKTREE="+s.Env.Worktree,
		"LANES_RESOURCE="+resource,
	)
	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("%s adapter %s: %s failed: %w: %s", KindScripts, s.Spec.Name, verb, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func lines(s string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			out = append(out, l)
		}
	}
	return out
}
