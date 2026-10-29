//go:build integration && darwin

package integration

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func (h *harness) dropPort(wt, name string) {
	h.t.Helper()
	h.editYAML(filepath.Join(wt, "lanes.yaml"), func(doc map[string]any) {
		ports := doc["ports"].(map[string]any)
		ports["names"] = slices.DeleteFunc(ports["names"].([]any), func(n any) bool { return n == name })
	})
}

func TestFirebaseMissingPortRefuses(t *testing.T) {
	h := newHarness(t, withProfile(firebaseLight))
	h.startDaemon()
	wt := h.worktree("noport", "feat/noport")
	h.dropPort(wt, "hub")

	r := h.lanes(wt, "start")
	if r.code == 0 || !strings.Contains(r.stderr, "ports.names is missing hub") {
		t.Fatalf("start without a hub port: %s", r)
	}
	if _, err := os.Stat(filepath.Join(h.tmpdir(wt), "pc.sock")); err == nil || h.laneOf(wt).State != "stopped" {
		t.Fatal("the refused start started the stack")
	}

	h.git(wt, "checkout", "--", "lanes.yaml")
	h.dropPort(wt, "test_hub")
	r = h.lanes(wt, "test-env", "--", "true")
	if r.code == 0 || !strings.Contains(r.stderr, "ports.names is missing test_hub") {
		t.Fatalf("test-env without a test_hub port: %s", r)
	}
}

func TestFirebaseGeneratedConfigs(t *testing.T) {
	h := newHarness(t, withProfile(firebaseLight))
	h.startDaemon()
	wt := h.worktree("gen", "feat/gen")
	stack, test := filepath.Join(wt, "firebase.lanes.json"), filepath.Join(wt, "firebase.lanes.test.json")
	hook := func(name string, env ...string) result {
		t.Helper()
		r := h.lanesIn(h.main, payload(wt), env, "hook", name)
		if r.code != 0 {
			t.Fatalf("hook %s: %s", name, r)
		}
		return r
	}

	hook("cwd-changed")
	first := mtimes(t, stack, test)
	time.Sleep(1100 * time.Millisecond)
	hook("cwd-changed")
	if again := mtimes(t, stack, test); !slices.Equal(first, again) {
		t.Fatalf("an unchanged reload rewrote the generated configs: %v then %v", first, again)
	}
	var cfg struct {
		Emulators map[string]any `json:"emulators"`
	}
	if err := json.Unmarshal([]byte(read(stack)), &cfg); err != nil {
		t.Fatal(err)
	}
	if fs, _ := cfg.Emulators["firestore"].(map[string]any); fs["host"] != "0.0.0.0" || int(fs["port"].(float64)) != h.port(wt, "firestore") {
		t.Fatalf("stack config firestore = %v", cfg.Emulators["firestore"])
	}

	if err := os.WriteFile(filepath.Join(wt, "firebase.emulator.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := h.lanes(wt, "start")
	if r.code == 0 || !strings.Contains(r.stderr, "firebase.emulator.json is not a valid firebase config") {
		t.Fatalf("start on an invalid base config: %s", r)
	}
	if exists(stack) || exists(test) {
		t.Fatal("the generated configs outlived an invalid base config")
	}

	h.git(wt, "checkout", "--", "firebase.emulator.json")
	hook("cwd-changed")
	if !exists(stack) || !exists(test) {
		t.Fatal("the configs did not come back with a valid base config")
	}
	if err := os.WriteFile(filepath.Join(wt, "firebase.emulator.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(t.TempDir(), "env")
	r = hook("session-start", "CLAUDE_ENV_FILE="+envFile)
	if !strings.Contains(r.stderr, "firebase.emulator.json") {
		t.Fatalf("the session-start hook did not report the invalid config: %s", r)
	}
	if !strings.Contains(read(envFile), "LANE_ID=") {
		t.Fatalf("the hook did not export the lane: %q", read(envFile))
	}
	if exists(stack) || exists(test) {
		t.Fatal("the hook left the generated configs of an invalid base config")
	}
}

func TestFirebaseSocketPathTooLong(t *testing.T) {
	deep := filepath.Join(os.TempDir(), "lanes-socket-"+strings.Repeat("d", 60))
	t.Cleanup(func() { os.RemoveAll(deep) })
	h := newHarness(t, withProfile(firebaseLight), withEnv("LANES_TMPDIR_ROOT="+deep))
	h.startDaemon()
	wt := h.worktree("socket", "feat/socket")
	for _, args := range [][]string{{"start"}, {"test-env", "--", "true"}} {
		r := h.lanes(wt, args...)
		if r.code == 0 || !strings.Contains(r.stderr, deep) || !strings.Contains(r.stderr, "fire_emu_") ||
			!strings.Contains(r.stderr, " bytes, past the 104-byte macOS limit, its final NUL included") {
			t.Errorf("lanes %s with a deep TMPDIR: %s", args[0], r)
		}
	}
}

func TestFirebaseToolsMissing(t *testing.T) {
	h := newHarness(t, withProfile(firebaseLight))
	h.startDaemon()
	wt := h.worktree("notools", "feat/notools")
	r := h.lanes(wt, "start")
	if r.code == 0 || !strings.Contains(r.stderr, "firebase-tools not found in node_modules") || !strings.Contains(r.stderr, "install the project's dependencies") {
		t.Fatalf("start without firebase-tools: %s", r)
	}
}

func TestFirebaseResourceLimits(t *testing.T) {
	h := newHarness(t, withProfile(firebaseLight))
	h.startDaemon()
	wt := h.worktree("limits", "feat/limits")
	cases := map[string]func(map[string]any){
		"resources[2].firebase: at most one firebase resource per project (first at resources[1])": func(doc map[string]any) {
			doc["resources"] = append(doc["resources"].([]any), map[string]any{"firebase": map[string]any{"config": "other.json"}})
		},
		"resources[1].firebase.export_timeout: 90s exceeds the 75s limit": func(doc map[string]any) {
			doc["resources"].([]any)[1].(map[string]any)["firebase"].(map[string]any)["export_timeout"] = "90s"
		},
	}
	for want, edit := range cases {
		h.git(wt, "checkout", "--", "lanes.yaml")
		h.editYAML(filepath.Join(wt, "lanes.yaml"), edit)
		for _, args := range [][]string{{"list"}, {"env"}, {"start"}, {"test-env", "--", "true"}} {
			if r := h.lanes(wt, args...); r.code == 0 || !strings.Contains(r.stderr, want) {
				t.Errorf("lanes %s: want a refusal naming %q, got %s", args[0], want, r)
			}
		}
	}
}

func TestTestEnvOnTheMainCheckout(t *testing.T) {
	h := newHarness(t, withProfile(firebaseLight))
	r := h.lanes(h.main, "test-env", "--", "sh", "-c", `echo "$TMPDIR|${LANES_FIREBASE_CONFIG-unset}"; exit 7`)
	if r.code != 7 || strings.TrimSpace(r.stdout) != os.Getenv("TMPDIR")+"|unset" || r.stderr != "" {
		t.Fatalf("test-env on the main checkout: %s", r)
	}
}

func defaultRouteIP(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("route", "-n", "get", "default").Output()
	if err != nil {
		t.Skip("no default route: the host tests need a network")
	}
	var iface string
	for _, l := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), ":"); ok && k == "interface" {
			iface = strings.TrimSpace(v)
		}
	}
	ip, err := exec.Command("ipconfig", "getifaddr", iface).Output()
	if err != nil || strings.TrimSpace(string(ip)) == "" {
		t.Skipf("no IPv4 address on %s", iface)
	}
	return strings.TrimSpace(string(ip))
}

func envValue(exports, name string) string {
	for _, l := range strings.Split(exports, "\n") {
		if v, ok := strings.CutPrefix(l, "export "+name+"="); ok {
			return strings.Trim(v, "'")
		}
	}
	return ""
}

func TestHostWithoutFirebase(t *testing.T) {
	ip := defaultRouteIP(t)
	h := newHarness(t)
	h.startDaemon()
	wt := h.worktree("host", "feat/host")
	h.session(wt)
	h.mustLanes(wt, "start")
	web := h.piecePID(wt, "web")

	h.mustLanes(wt, "host", "lan")
	if got := envValue(h.mustLanes(wt, "env"), "LANES_HOST"); got != ip {
		t.Fatalf("LANES_HOST = %q after host lan, want %s", got, ip)
	}
	if !strings.Contains(read(filepath.Join(wt, "app.env")), "LANES_HOST="+ip+"\n") {
		t.Fatalf("app.env = %q", read(filepath.Join(wt, "app.env")))
	}
	if h.piecePID(wt, "web") != web {
		t.Fatal("host lan restarted a piece")
	}

	r := h.lanesIn(wt, "", []string{"LANES_LAN_PROBE=unreachable"}, "host", "lan")
	if r.code == 0 || !strings.Contains(r.stderr, "no network") || h.laneOf(wt).Host != ip {
		t.Fatalf("host lan without a network: %s, host %s", r, h.laneOf(wt).Host)
	}

	h.mustLanes(wt, "stop")
	h.mustLanes(wt, "start")
	if l := h.laneOf(wt); l.Host != ip {
		t.Fatalf("the host did not survive a restart: %+v", l)
	}
	if !strings.Contains(h.mustLanes(h.main, "list"), "host      "+ip+"\n") {
		t.Fatal("lanes list does not show the lane's host")
	}

	h.mustLanes(wt, "host", "local")
	if got := envValue(h.mustLanes(wt, "env"), "LANES_HOST"); got != "127.0.0.1" || !strings.Contains(read(filepath.Join(wt, "app.env")), "LANES_HOST=127.0.0.1\n") {
		t.Fatalf("host local left LANES_HOST = %q", got)
	}

	h.mustLanes(wt, "host", "lan")
	h.git(wt, "switch", "-q", "-c", "feat/next", "origin/main")
	h.mustLanes(wt, "env")
	if l := h.laneOf(wt); l.Host != "127.0.0.1" {
		t.Fatalf("a new lane kept the old lane's host: %+v", l)
	}
}

func mtimes(t *testing.T, files ...string) []time.Time {
	t.Helper()
	var out []time.Time
	for _, f := range files {
		info, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, info.ModTime())
	}
	return out
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
