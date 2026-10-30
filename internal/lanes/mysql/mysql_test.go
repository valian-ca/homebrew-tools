package mysql

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/lanes/config"
	"github.com/valian-ca/homebrew-tools/internal/lanes/procinfo"
)

var onr = config.Databases{{Key: "dev", Template: "nezrouge_{lane}"}, {Key: "test", Template: "nezrouge_test_{lane}"}}

func TestOwners(t *testing.T) {
	cases := map[string][]string{
		"nezrouge_feat_x_abc123":   {"feat_x_abc123"},
		"nezrouge_test_foo_abc123": {"test_foo_abc123", "foo_abc123"},
		"nezrouge":                 nil,
		"nezrouge_test":            nil,
		"nezrouge_scratch":         nil,
		"nezrouge_foo_ABC123":      nil,
		"nezrouge_foo_abc12":       nil,
		"nezrouge__abc123":         nil,
		"other_feat_x_abc123":      nil,
		"nezrouge_" + strings.Repeat("a", 25) + "_abc123": nil,
	}
	for name, want := range cases {
		if got := Owners(onr, name); !slices.Equal(got, want) {
			t.Errorf("Owners(%q) = %v, want %v", name, got, want)
		}
	}
	twice := config.Databases{{Key: "a", Template: "x_{lane}_y_{lane}"}}
	if got := Owners(twice, "x_l_abc123_y_l_abc123"); !slices.Equal(got, []string{"l_abc123"}) {
		t.Errorf("a template naming the lane twice: %v", got)
	}
	if got := Owners(twice, "x_l_abc123_y_m_abc123"); got != nil {
		t.Errorf("two different lanes in one name: %v", got)
	}
}

func TestVars(t *testing.T) {
	spec := config.MySQL{Port: 3316, User: "root", Databases: onr}
	got := Vars(spec, "feat_x_abc123").Map()
	want := map[string]string{
		"LANES_MYSQL_HOST":          "127.0.0.1",
		"LANES_MYSQL_PORT":          "3316",
		"LANES_MYSQL_USER":          "root",
		"LANES_MYSQL_PASSWORD":      "",
		"LANES_MYSQL_DATABASE_DEV":  "nezrouge_feat_x_abc123",
		"LANES_MYSQL_DATABASE_TEST": "nezrouge_test_feat_x_abc123",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if _, ok := got["LANES_MYSQL_PASSWORD"]; !ok {
		t.Error("an empty password is not exported")
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func server(port int, procs procinfo.Source, start string) *Server {
	return &Server{
		Spec:  config.MySQL{Port: port, User: "root", Start: start, Listeners: []string{"mysqld"}, Databases: onr},
		Procs: procs,
	}
}

func TestGuard(t *testing.T) {
	fake := &procinfo.Fake{
		Procs:     []procinfo.Proc{{PID: 42, Comm: "cloud-sql-proxy"}, {PID: 43, Comm: "com.docker.back", Argv: []string{"/Applications/Docker.app/Contents/MacOS/com.docker.backend"}}},
		Listeners: map[int]int{1: 42, 2: 43},
	}
	err := server(1, fake, "").Guard()
	var ge *GuardError
	if !errors.As(err, &ge) || !strings.Contains(err.Error(), "cloud-sql-proxy (PID 42)") {
		t.Fatalf("a foreign listener: %v", err)
	}
	s := server(2, fake, "")
	s.Spec.Listeners = []string{"com.docker.backend"}
	if err := s.Guard(); err != nil {
		t.Fatalf("Docker Desktop's backend, named past the kernel's truncation: %v", err)
	}
	if err := server(freePort(t), fake, "").Guard(); !errors.Is(err, ErrAsleep) {
		t.Fatalf("nobody listening: %v, want ErrAsleep", err)
	}
}

func TestGuardRefusesAListenerItCannotSee(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	err = server(l.Addr().(*net.TCPAddr).Port, &procinfo.Fake{}, "").Guard()
	var ge *GuardError
	if !errors.As(err, &ge) || ge.PID != 0 {
		t.Fatalf("an invisible listener: %v", err)
	}
}

func wake(t *testing.T, s *Server, timeout time.Duration) error {
	t.Helper()
	dir := t.TempDir()
	return s.Wake(context.Background(), WakeOptions{Dir: dir, Lock: filepath.Join(dir, "lock"), Environ: os.Environ(), Timeout: timeout})
}

func TestWakeWithoutStartNamesTheAddress(t *testing.T) {
	port := freePort(t)
	err := wake(t, server(port, &procinfo.Fake{}, ""), time.Second)
	if err == nil || !strings.Contains(err.Error(), net.JoinHostPort("127.0.0.1", strconv.Itoa(port))) || !strings.Contains(err.Error(), "no start command") {
		t.Fatalf("a sleeping server without start: %v", err)
	}
}

func TestWakeReportsTheEndOfAFailedStart(t *testing.T) {
	err := wake(t, server(freePort(t), &procinfo.Fake{}, "echo first; echo boom >&2; exit 3"), time.Second)
	if err == nil || !strings.Contains(err.Error(), "boom") || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("a failed start: %v", err)
	}
}

func TestWakeTimesOutWithTheStartOutput(t *testing.T) {
	err := wake(t, server(freePort(t), &procinfo.Fake{}, "echo started nothing"), 500*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "did not accept connections within 500ms") || !strings.Contains(err.Error(), "started nothing") {
		t.Fatalf("a start that brings no server: %v", err)
	}
}

func TestWakeRunsStartFromDirWithItsEnvironment(t *testing.T) {
	dir := t.TempDir()
	s := server(freePort(t), &procinfo.Fake{}, `pwd > where; echo "${PROBE:-unset}" >> where`)
	_ = s.Wake(context.Background(), WakeOptions{Dir: dir, Lock: filepath.Join(dir, "lock"), Environ: []string{"PATH=/usr/bin:/bin", "PROBE=kept"}, Timeout: 100 * time.Millisecond})
	data, err := os.ReadFile(filepath.Join(dir, "where"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(string(data)); len(got) != 2 || !strings.HasSuffix(got[0], filepath.Base(dir)) || got[1] != "kept" {
		t.Fatalf("start ran as %q", data)
	}
}
