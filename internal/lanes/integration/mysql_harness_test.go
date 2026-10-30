//go:build integration && darwin

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"

	"github.com/valian-ca/homebrew-tools/internal/lanes/registry"
)

// mysqlSlots bounds the MySQL servers running at once, each a few hundred
// megabytes even with the demo's small buffer pool.
var mysqlSlots = make(chan struct{}, 3)

func requireMySQL(t *testing.T) {
	t.Helper()
	if mysqldPath() == "" {
		if os.Getenv("LANES_REQUIRE_MYSQL") == "1" {
			t.Fatal("mysqld is required for the MySQL cases (LANES_REQUIRE_MYSQL=1)")
		}
		t.Skip("mysqld not found: the MySQL cases need brew install mysql@8.4")
	}
}

func mysqldPath() string {
	if p, err := exec.LookPath("mysqld"); err == nil {
		return p
	}
	out, err := exec.Command("brew", "--prefix", "mysql@8.4").Output()
	if err != nil {
		return ""
	}
	p := filepath.Join(strings.TrimSpace(string(out)), "bin", "mysqld")
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// freePort takes an ephemeral port, above the range the harnesses give lanes.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// trimMySQL turns the demo back into a schema-1 lanes.yaml: what lanes 0.2.0
// read, and what lanes 0.3.0 must still run unchanged.
func trimMySQL(doc map[string]any) {
	doc["schema"] = 1
	delete(doc, "env")
	doc["resources"] = slices.DeleteFunc(doc["resources"].([]any), func(r any) bool { return r.(map[string]any)["mysql"] != nil })
	doc["generate"] = slices.DeleteFunc(doc["generate"].([]any), func(g any) bool { return g.(map[string]any)["template"] == "db.env.tmpl" })
}

func (h *harness) pointMySQL(doc map[string]any) {
	m := mysqlSpecIn(doc)
	m["port"] = h.mysqlPort
	m["start"] = "tool/mysql-server " + strconv.Itoa(h.mysqlPort)
}

func mysqlSpecIn(doc map[string]any) map[string]any {
	for _, r := range doc["resources"].([]any) {
		if m, ok := r.(map[string]any)["mysql"].(map[string]any); ok {
			return m
		}
	}
	return nil
}

func (h *harness) mysqlSpec(wt string, edit func(map[string]any)) {
	h.t.Helper()
	h.editYAML(filepath.Join(wt, "lanes.yaml"), func(doc map[string]any) { edit(mysqlSpecIn(doc)) })
}

func pidfile(repo string) string { return filepath.Join(repo, ".demo", "mysql", "mysqld.pid") }

func (h *harness) mysqldPID() int {
	for _, r := range h.repos {
		if data, err := os.ReadFile(pidfile(r)); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && syscall.Kill(pid, 0) == nil {
				return pid
			}
		}
	}
	return 0
}

// stopMySQL ends the server by the PID its pid file names, never by name.
func (h *harness) stopMySQL() {
	h.t.Helper()
	pid := h.mysqldPID()
	if pid == 0 {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)
	h.eventually("mysqld to exit", time.Minute, func() bool { return syscall.Kill(pid, 0) != nil && !listening(h.mysqlPort) })
}

func (h *harness) stopMySQLServers() {
	for _, r := range h.repos {
		data, err := os.ReadFile(pidfile(r))
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			continue
		}
		_ = syscall.Kill(pid, syscall.SIGTERM)
		for i := 0; i < 300 && syscall.Kill(pid, 0) == nil; i++ {
			time.Sleep(100 * time.Millisecond)
		}
	}
}

// startMySQL starts the server as the project's own commands would, from the
// main checkout.
func (h *harness) startMySQL() {
	h.t.Helper()
	h.run(h.main, filepath.Join(h.main, "tool", "mysql-server"), strconv.Itoa(h.mysqlPort))
	h.eventually("mysqld to accept connections", time.Minute, func() bool {
		db := h.db("")
		defer db.Close()
		return db.Ping() == nil
	})
}

func (h *harness) db(name string) *sql.DB {
	h.t.Helper()
	c := driver.NewConfig()
	c.Net, c.Addr, c.User, c.DBName = "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(h.mysqlPort)), "root", name
	c.Timeout = 2 * time.Second
	conn, err := driver.NewConnector(c)
	if err != nil {
		h.t.Fatal(err)
	}
	return sql.OpenDB(conn)
}

func (h *harness) exec(name, query string) {
	h.t.Helper()
	db := h.db(name)
	defer db.Close()
	if _, err := db.ExecContext(context.Background(), query); err != nil {
		h.t.Fatalf("%s: %v", query, err)
	}
}

func (h *harness) databases() []string {
	h.t.Helper()
	db := h.db("")
	defer db.Close()
	rows, err := db.Query("SHOW DATABASES")
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			h.t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

func (h *harness) hasDB(name string) bool { return slices.Contains(h.databases(), name) }

func (h *harness) tables(name string) []string {
	h.t.Helper()
	db := h.db("")
	defer db.Close()
	rows, err := db.Query("SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME", name)
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			h.t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

func devDB(lane string) string  { return "lanes_demo_" + lane }
func testDB(lane string) string { return "lanes_demo_test_" + lane }

func starts(repo string) int {
	return strings.Count(read(filepath.Join(repo, ".demo", "mysql-start.log")), "begin ")
}

func (h *harness) seedLog(lane string) []string {
	data := strings.TrimSpace(read(filepath.Join(h.store, "mysql-seed", lane+".log")))
	if data == "" {
		return nil
	}
	return strings.Split(data, "\n")
}

func (h *harness) registered(repo, adapter string) []registry.Entry {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(repo, ".git", "lanes", "registry.json"))
	if err != nil {
		return nil
	}
	var f struct {
		Entries []registry.Entry `json:"entries"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		h.t.Fatal(err)
	}
	return slices.DeleteFunc(f.Entries, func(e registry.Entry) bool { return e.Adapter != adapter })
}

func (h *harness) registeredDBs(repo string) []string {
	var out []string
	for _, e := range h.registered(repo, "mysql") {
		out = append(out, e.Resource)
	}
	slices.Sort(out)
	return out
}
