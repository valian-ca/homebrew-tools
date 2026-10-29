package pc

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestParseVersion(t *testing.T) {
	for in, want := range map[string]Version{
		"v1.124.0":                          {1, 124, 0},
		"Process Compose\nVersion: v1.73.0": {1, 73, 0},
		"1.2.3-rc1":                         {1, 2, 3},
	} {
		got, ok := ParseVersion(in)
		if !ok || got != want {
			t.Errorf("ParseVersion(%q) = %v, %v", in, got, ok)
		}
	}
	if !(Version{1, 72, 9}).Less(MinVersion) || (Version{1, 73, 0}).Less(MinVersion) || (Version{2, 0, 0}).Less(MinVersion) {
		t.Error("Less disagrees with the minimum")
	}
}

func fakeBinary(t *testing.T, output string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\necho '" + output + "'\n"
	if err := os.WriteFile(filepath.Join(dir, Binary), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":/usr/bin:/bin")
}

func TestResolveRefusesAnOldProcessCompose(t *testing.T) {
	fakeBinary(t, "Version: v1.2.0")
	_, err := Resolve(context.Background())
	var ve *VersionError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{"1.2.0", MinVersion.String()} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q does not name %s", err, want)
		}
	}
}

func TestResolveAcceptsARecentProcessCompose(t *testing.T) {
	fakeBinary(t, "v1.124.0")
	path, err := Resolve(context.Background())
	if err != nil || filepath.Base(path) != Binary {
		t.Fatalf("Resolve = %q, %v", path, err)
	}
}

func TestEvaluate(t *testing.T) {
	running := State{Name: "web", Status: StatusRunning}
	probing := State{Name: "api", Status: StatusRunning, HasHealthProbe: true, Health: "Not Ready"}
	cases := []struct {
		name   string
		states []State
		ready  bool
		piece  string
	}{
		{"all running", []State{running, {Name: "x", Status: StatusDisabled}, {Name: "seed", Status: StatusCompleted}}, true, ""},
		{"probe pending", []State{running, probing}, false, ""},
		{"probe ready", []State{running, {Name: "api", Status: StatusRunning, HasHealthProbe: true, Health: HealthReady}}, true, ""},
		{"pending", []State{{Name: "db", Status: "Pending"}}, false, ""},
		{"crashed", []State{running, {Name: "crashy", Status: StatusCompleted, ExitCode: 3}}, false, "crashy"},
		{"error", []State{{Name: "bad", Status: StatusError}}, false, "bad"},
		{"restarted", []State{{Name: "flappy", Status: StatusRunning, Restarts: 1}}, false, "flappy"},
		{"skipped", []State{{Name: "dep", Status: StatusSkipped}}, false, "dep"},
	}
	for _, c := range cases {
		ready, fail := Evaluate(c.states)
		got := ""
		if fail != nil {
			got = fail.Piece
		}
		if ready != c.ready || got != c.piece {
			t.Errorf("%s: ready=%v fail=%q, want %v %q", c.name, ready, got, c.ready, c.piece)
		}
	}
}

func TestKilledForMemory(t *testing.T) {
	if !KilledForMemory(State{ExitCode: 137}, "") {
		t.Error("SIGKILL exit not recognized")
	}
	if !KilledForMemory(State{ExitCode: 1}, "java.lang.OutOfMemoryError: Java heap space") {
		t.Error("JVM OOM not recognized")
	}
	if KilledForMemory(State{ExitCode: 3}, "cannot open the database") {
		t.Error("an ordinary failure reported as OOM")
	}
}

func TestFragment(t *testing.T) {
	data, err := Fragment([]string{"web", "emulators"}, map[string]Process{"emulators": {"command": "firebase emulators:start", "shutdown": map[string]any{"parent_only": true}}}, "/tmp/lanes-1-x/logs")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		OrderedShutdown bool                      `yaml:"ordered_shutdown"`
		Processes       map[string]map[string]any `yaml:"processes"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if !doc.OrderedShutdown {
		t.Error("ordered_shutdown missing")
	}
	if doc.Processes["web"]["log_location"] != "/tmp/lanes-1-x/logs/web.log" {
		t.Errorf("web = %v", doc.Processes["web"])
	}
	if _, ok := doc.Processes["web"]["command"]; ok {
		t.Error("the fragment redeclares a project process's command")
	}
	em := doc.Processes["emulators"]
	if em["command"] != "firebase emulators:start" || em["log_location"] != "/tmp/lanes-1-x/logs/emulators.log" {
		t.Errorf("emulators = %v", em)
	}
}

func TestProjectProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), ProjectFile)
	if err := os.WriteFile(path, []byte("version: \"0.5\"\nprocesses:\n  web: {command: a}\n  api: {command: b}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ProjectProcesses(path)
	if err != nil || strings.Join(got, ",") != "api,web" {
		t.Fatalf("ProjectProcesses = %v, %v", got, err)
	}
}

type fakeServer struct {
	mu      sync.Mutex
	states  []State
	calls   []string
	stopped chan struct{}
}

func serveFake(t *testing.T, f *fakeServer) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "pc.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	f.stopped = make(chan struct{})
	mux := http.NewServeMux()
	record := func(r *http.Request) {
		f.mu.Lock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		f.mu.Unlock()
	}
	mux.HandleFunc("GET /live", func(w http.ResponseWriter, r *http.Request) {})
	mux.HandleFunc("GET /processes", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"data": f.states})
	})
	mux.HandleFunc("POST /process/start/{name}", func(w http.ResponseWriter, r *http.Request) { record(r) })
	mux.HandleFunc("POST /process/restart/{name}", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		http.Error(w, `{"error":"process nope does not exist"}`, http.StatusBadRequest)
	})
	srv := &http.Server{Handler: mux}
	mux.HandleFunc("POST /project/stop", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		go func() {
			time.Sleep(100 * time.Millisecond)
			srv.Close()
			os.Remove(sock)
			close(f.stopped)
		}()
	})
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { srv.Close() })
	return sock
}

func TestClient(t *testing.T) {
	f := &fakeServer{states: []State{{Name: "web", Status: StatusRunning, PID: 42}}}
	c := NewClient(serveFake(t, f))
	ctx := context.Background()

	if !c.Alive(ctx) {
		t.Fatal("server not alive")
	}
	states, err := c.States(ctx)
	if err != nil || len(states) != 1 || states[0].PID != 42 {
		t.Fatalf("States = %+v, %v", states, err)
	}
	if err := c.Start(ctx, "extra"); err != nil {
		t.Fatal(err)
	}
	if err := c.Restart(ctx, "nope"); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("Restart error = %v", err)
	}
	downCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := c.Down(downCtx); err != nil {
		t.Fatal(err)
	}
	if c.Alive(ctx) {
		t.Fatal("still alive after Down")
	}
	if strings.Join(f.calls, ",") != "POST /process/start/extra,POST /process/restart/nope,POST /project/stop" {
		t.Errorf("calls = %v", f.calls)
	}
}
