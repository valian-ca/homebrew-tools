package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"slices"
	"strings"
	"testing"

	"github.com/valian-ca/homebrew-tools/internal/lanes/config"
	"github.com/valian-ca/homebrew-tools/internal/lanes/mysql"
	"github.com/valian-ca/homebrew-tools/internal/lanes/procinfo"
	"github.com/valian-ca/homebrew-tools/internal/lanes/registry"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func mysqlAdapter(t *testing.T, procs procinfo.Source) *MySQL {
	t.Helper()
	spec := config.MySQL{Host: "127.0.0.1", Port: freePort(t), User: "root", Listeners: []string{"mysqld"},
		Databases: config.Databases{{Key: "dev", Template: "app_{lane}"}, {Key: "test", Template: "app_test_{lane}"}}}
	m := NewMySQL(spec, Env{})
	m.Server.Procs = procs
	return m
}

func TestMySQLEraseRefusesAnotherLanesDatabase(t *testing.T) {
	err := mysqlAdapter(t, &procinfo.Fake{}).Erase(context.Background(), "other_abc123", "app_feat_abc123")
	if err == nil || !strings.Contains(err.Error(), "is not a database of lane other_abc123") || errors.Is(err, ErrDeferred) {
		t.Fatalf("erasing another lane's database: %v", err)
	}
	if err := mysqlAdapter(t, &procinfo.Fake{}).Erase(context.Background(), "x_abc123", "app"); err == nil || errors.Is(err, ErrDeferred) {
		t.Fatalf("erasing the main checkout's database: %v", err)
	}
}

func TestMySQLEraseAndListDeferWithoutTheServer(t *testing.T) {
	ctx := context.Background()
	asleep := mysqlAdapter(t, &procinfo.Fake{})
	if err := asleep.Erase(ctx, "feat_abc123", "app_feat_abc123"); !errors.Is(err, ErrDeferred) || !errors.Is(err, mysql.ErrAsleep) {
		t.Fatalf("erase, server asleep: %v", err)
	}
	if _, err := asleep.List(ctx); !errors.Is(err, ErrDeferred) {
		t.Fatalf("list, server asleep: %v", err)
	}

	fake := &procinfo.Fake{Procs: []procinfo.Proc{{PID: 42, Comm: "cloud-sql-proxy"}}}
	guarded := mysqlAdapter(t, fake)
	fake.Listeners = map[int]int{guarded.Spec.Port: 42}
	err := guarded.Erase(ctx, "feat_abc123", "app_feat_abc123")
	var ge *mysql.GuardError
	if !errors.Is(err, ErrDeferred) || !errors.As(err, &ge) || ge.PID != 42 {
		t.Fatalf("erase behind a foreign listener: %v", err)
	}
}

func TestMySQLEntryRebuildsTheAdapter(t *testing.T) {
	m := mysqlAdapter(t, &procinfo.Fake{})
	en := m.Entry("feat_abc123", "app_feat_abc123")
	if en.Kind != KindMySQL || en.Adapter != KindMySQL || en.MySQL == nil {
		t.Fatalf("entry = %+v", en)
	}
	data, err := json.Marshal(en)
	if err != nil {
		t.Fatal(err)
	}
	var back registry.Entry
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	a, err := FromEntry(back, Env{})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.(*MySQL).Owners("app_test_feat_abc123"); !slices.Equal(got, []string{"test_feat_abc123", "feat_abc123"}) {
		t.Fatalf("the rebuilt adapter lost its templates: owners %v", got)
	}
	back.MySQL = nil
	if _, err := FromEntry(back, Env{}); err == nil {
		t.Fatal("a mysql entry without its spec was rebuilt")
	}
}

func TestFromConfigBuildsMySQL(t *testing.T) {
	cfg, err := config.Parse([]byte("schema: 2\nports: {block: 2, names: [web]}\nresources:\n  - mysql:\n      databases:\n        dev: app_{lane}\n"))
	if err != nil {
		t.Fatal(err)
	}
	as := FromConfig(cfg, Env{})
	if len(as) != 1 || as[0].Kind() != KindMySQL || as[0].Processes() != nil {
		t.Fatalf("adapters = %+v", as)
	}
	if _, ok := as[0].(Ensurer); !ok {
		t.Error("the mysql adapter is not an Ensurer")
	}
	if _, ok := as[0].(MultiOwner); !ok {
		t.Error("the mysql adapter is not a MultiOwner")
	}
}
