package adapter

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/lanes/config"
	"github.com/valian-ca/homebrew-tools/internal/lanes/mysql"
	"github.com/valian-ca/homebrew-tools/internal/lanes/paths"
	"github.com/valian-ca/homebrew-tools/internal/lanes/pc"
	"github.com/valian-ca/homebrew-tools/internal/lanes/registry"
)

const KindMySQL = "mysql"

type MySQL struct {
	Spec   config.MySQL
	Server *mysql.Server
	Env    Env
}

func NewMySQL(spec config.MySQL, e Env) *MySQL {
	return &MySQL{Spec: spec, Server: mysql.New(spec), Env: e}
}

func (m *MySQL) Kind() string { return KindMySQL }

func (m *MySQL) Name() string { return KindMySQL }

// Preflight leaves the server alone: lanesd runs it at every launch, and only
// lanes start may wake the server.
func (m *MySQL) Preflight(context.Context) error { return nil }

func (m *MySQL) Processes() map[string]pc.Process { return nil }

func (m *MySQL) Create(ctx context.Context, lane string) ([]string, error) {
	var names []string
	err := m.Ensure(ctx, lane, func(n string) error {
		names = append(names, n)
		return nil
	})
	return names, err
}

func (m *MySQL) Ensure(ctx context.Context, lane string, register func(string) error) error {
	if err := m.Server.Wake(ctx, mysql.WakeOptions{
		Dir:     m.Env.MainRoot,
		Lock:    paths.MySQLStartLock(m.Spec.Port),
		Environ: m.Env.WithoutLane,
		Timeout: mysql.WakeTimeout(),
	}); err != nil {
		return err
	}
	return m.Server.Ensure(ctx, mysql.Names(m.Spec.Databases, lane), register)
}

// Erase never wakes the server: one that sleeps, does not answer or sits
// behind a foreign listener defers the erase to a later reconcile.
func (m *MySQL) Erase(ctx context.Context, lane, resource string) error {
	if !slices.Contains(m.Owners(resource), lane) {
		return fmt.Errorf("%s adapter: %s is not a database of lane %s — not erased", KindMySQL, resource, lane)
	}
	db, err := m.Server.Connect(ctx)
	if err != nil {
		return fmt.Errorf("%w: %s adapter: %s: %w", ErrDeferred, KindMySQL, resource, err)
	}
	defer db.Close()
	if err := m.Server.Drop(ctx, db, resource); errors.Is(err, mysql.ErrUnreachable) {
		return fmt.Errorf("%w: %s adapter: %w", ErrDeferred, KindMySQL, err)
	} else if err != nil {
		return err
	}
	return nil
}

func (m *MySQL) List(ctx context.Context) ([]string, error) {
	all, err := m.Server.Databases(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %s adapter: list: %w", ErrDeferred, KindMySQL, err)
	}
	return slices.DeleteFunc(all, func(n string) bool { return len(m.Owners(n)) == 0 }), nil
}

func (m *MySQL) Owner(_ context.Context, resource string) (string, error) {
	if o := m.Owners(resource); len(o) > 0 {
		return o[0], nil
	}
	return "", nil
}

func (m *MySQL) Owners(resource string) []string { return mysql.Owners(m.Spec.Databases, resource) }

func (m *MySQL) Entry(lane, resource string) registry.Entry {
	spec := m.Spec
	return registry.Entry{
		Kind:      KindMySQL,
		Adapter:   KindMySQL,
		Resource:  resource,
		Lane:      lane,
		Worktree:  m.Env.Worktree,
		CreatedAt: time.Now().UTC(),
		MySQL:     &spec,
	}
}
