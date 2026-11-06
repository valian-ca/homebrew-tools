//go:build integration && darwin

package integration

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/valian-ca/homebrew-tools/internal/lanes/config"
	"github.com/valian-ca/homebrew-tools/internal/lanes/firebase"
)

var laneFiles = []string{"lanes.yaml", "process-compose.yaml", ".envrc", ".claude/settings.json"}

func (h *harness) unadopt(dir string) {
	h.t.Helper()
	h.git(dir, append([]string{"rm", "-q"}, laneFiles...)...)
	h.commitAll(dir, "a project without lanes")
}

func requireNode(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"node", "npm"} {
		if _, err := exec.LookPath(tool); err != nil {
			if os.Getenv("LANES_REQUIRE_FIREBASE") == "1" {
				t.Fatalf("%s is required for the Node cases (LANES_REQUIRE_FIREBASE=1)", tool)
			}
			t.Skipf("%s not found: the dev-server case needs Node", tool)
		}
	}
}

func TestInitOnTheDemo(t *testing.T) {
	h := newHarness(t)
	h.unadopt(h.main)
	report := h.mustLanes(h.main, "init")

	cfg, err := config.Load(h.main)
	if err != nil {
		t.Fatal(err)
	}
	fb := cfg.Firebase()
	if cfg.Schema != 2 || fb == nil || fb.Config != "firebase.emulator.json" {
		t.Fatalf("lanes.yaml: schema %d, firebase %+v", cfg.Schema, fb)
	}
	base, err := firebase.LoadBase(h.main, fb)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []firebase.Series{firebase.Stack, firebase.Test} {
		if missing := firebase.Missing(firebase.NewPlan(base, fb, s).RequiredPorts(), cfg.Ports.Names); len(missing) > 0 {
			t.Errorf("the %s lacks the ports %v", s, missing)
		}
	}
	if !slices.ContainsFunc(cfg.Setup, func(s config.Step) bool {
		return s.Run == "npm ci" && slices.Equal(s.WhenChanged, []string{"package-lock.json"})
	}) {
		t.Errorf("no npm ci step on package-lock.json: %+v", cfg.Setup)
	}
	settings := read(filepath.Join(h.main, ".claude/settings.json"))
	for _, hook := range []string{"session-start", "cwd-changed", "session-end"} {
		if !strings.Contains(settings, "command -v lanes >/dev/null 2>&1 && lanes hook "+hook+" || true") {
			t.Errorf("settings.json lacks the guarded %s hook:\n%s", hook, settings)
		}
	}
	if envrc := read(filepath.Join(h.main, ".envrc")); envrc != "if has lanes; then eval \"$(lanes env)\"; fi\n" {
		t.Errorf(".envrc %q", envrc)
	}
	for _, want := range []string{"resources[0].firebase.seed", "lanes.yaml generate", "a scripts resource", "a mysql resource, if the project's MySQL server runs without docker-compose"} {
		if !strings.Contains(report, want) {
			t.Errorf("the report does not name %q:\n%s", want, report)
		}
	}
}

func TestInitInAWorktree(t *testing.T) {
	h := newHarness(t)
	h.unadopt(h.main)
	wt := h.worktree("adopt", "feat/adopt")
	h.mustLanes(filepath.Join(wt, "functions"), "init")
	for _, f := range laneFiles {
		if !exists(filepath.Join(wt, f)) {
			t.Errorf("%s was not written at the worktree's root", f)
		}
		if exists(filepath.Join(h.main, f)) {
			t.Errorf("%s was written in the main checkout", f)
		}
	}
	if s := h.git(h.main, "status", "--porcelain", "--untracked-files=all", "--", ".", ":!.claude/worktrees"); s != "" {
		t.Errorf("the main checkout changed:\n%s", s)
	}
}

func TestInitDevServerStartsOnItsPort(t *testing.T) {
	requireNode(t)
	h := newHarness(t)
	repo := filepath.Join(filepath.Dir(h.main), "vite-app")
	if err := os.MkdirAll(filepath.Join(repo, "frontend"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"package.json", "package-lock.json", "index.html"} {
		data, err := os.ReadFile(filepath.Join("testdata", "vite", f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, "frontend", f), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("node_modules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.git(repo, "init", "-q", "-b", "main")
	h.commitAll(repo, "a vite app")

	report := h.mustLanes(repo, "init")
	var procs struct {
		Processes map[string]struct {
			Command  string `yaml:"command"`
			Disabled bool   `yaml:"disabled"`
		} `yaml:"processes"`
	}
	if err := yaml.Unmarshal([]byte(read(filepath.Join(repo, "process-compose.yaml"))), &procs); err != nil {
		t.Fatal(err)
	}
	dev, sb := procs.Processes["frontend-dev"], procs.Processes["frontend-storybook"]
	if len(procs.Processes) != 2 || !dev.Disabled || !sb.Disabled {
		t.Fatalf("processes %+v", procs.Processes)
	}
	if dev.Command != `npm run dev -- --port "$PORT_FRONTEND_DEV"` || sb.Command != `npm exec -- storybook dev --port "$PORT_FRONTEND_STORYBOOK"` {
		t.Errorf("commands %q and %q", dev.Command, sb.Command)
	}
	if !strings.Contains(report, "frontend-storybook: rewritten") {
		t.Errorf("the report does not list frontend-storybook as rewritten:\n%s", report)
	}
	h.commitAll(repo, "adopt lanes")

	h.startDaemon()
	wt := h.worktreeIn(repo, "vite", "feat/vite")
	h.session(wt)
	h.mustLanes(wt, "start")
	h.mustLanes(wt, "up", "frontend-dev")
	url := "http://localhost:" + strconv.Itoa(h.port(wt, "frontend_dev")) + "/"
	deadline := time.Now().Add(time.Minute)
	for {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("Vite does not answer on %s: %v\n%s", url, err, h.pieceLog(wt, "frontend-dev"))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestInitFilledInStartsTheStack(t *testing.T) {
	h := newHarness(t, withProfile(fullStack))
	var demo map[string]any
	if err := yaml.Unmarshal([]byte(read(filepath.Join(h.main, "lanes.yaml"))), &demo); err != nil {
		t.Fatal(err)
	}
	demoProcs := read(filepath.Join(h.main, "process-compose.yaml"))
	h.unadopt(h.main)
	report := h.mustLanes(h.main, "init")
	for _, want := range []string{"resources[0].firebase.seed", "a scripts resource", "lanes.yaml generate", "process-compose.yaml processes"} {
		if !strings.Contains(report, want) {
			t.Fatalf("the report does not name %q:\n%s", want, report)
		}
	}

	resource := func(kind string) any {
		for _, r := range demo["resources"].([]any) {
			if m := r.(map[string]any); m[kind] != nil {
				return m
			}
		}
		t.Fatalf("the demo has no %s resource", kind)
		return nil
	}
	h.editYAML(filepath.Join(h.main, "lanes.yaml"), func(doc map[string]any) {
		resources := doc["resources"].([]any)
		resources[0].(map[string]any)["firebase"].(map[string]any)["seed"] = "seed"
		doc["resources"] = append([]any{resource("scripts")}, append(resources, resource("mysql"))...)
		doc["env"] = demo["env"]
		doc["generate"] = demo["generate"]
		doc["shared"] = demo["shared"]
		ports := doc["ports"].(map[string]any)
		ports["names"] = append([]any{"web", "extra"}, ports["names"].([]any)...)
		ports["block"] = 30
	})
	if err := os.WriteFile(filepath.Join(h.main, "process-compose.yaml"), []byte(demoProcs), 0o644); err != nil {
		t.Fatal(err)
	}
	h.commitAll(h.main, "fill in what lanes init reported")

	h.startDaemon()
	wt := h.worktree("adopted", "feat/adopted")
	h.session(wt)
	h.mustLanes(wt, "start")
	if !h.hasDoc(wt, "demo/hello") {
		t.Error("the filled-in seed was not imported")
	}
	if !h.hasDB(devDB(h.laneOf(wt).Lane)) {
		t.Error("the lane's dev database was not made")
	}
}
