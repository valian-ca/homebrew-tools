package adapter

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/valian-ca/homebrew-tools/internal/lanes/config"
)

// The demo's own store script is the protocol fixture: it is what the
// contract shows projects.
func demoStore(t *testing.T) *Scripts {
	t.Helper()
	demo, err := filepath.Abs("../../../examples/lanes-demo")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LANES_DEMO_STATE", t.TempDir())
	return &Scripts{
		Spec: config.Scripts{
			Name:   "demo-store",
			Create: "tool/lane-store create",
			Erase:  "tool/lane-store erase",
			List:   "tool/lane-store list",
			Owner:  "tool/lane-store owner",
		},
		Env: Env{Dir: demo, Worktree: "/somewhere/wt"},
	}
}

func TestScriptsProtocol(t *testing.T) {
	s := demoStore(t)
	ctx := context.Background()

	names, err := s.Create(ctx, "feat_a_123abc")
	if err != nil || strings.Join(names, ",") != "feat_a_123abc" {
		t.Fatalf("Create = %v, %v", names, err)
	}
	token, _ := os.ReadFile(filepath.Join(os.Getenv("LANES_DEMO_STATE"), "feat_a_123abc", "data.txt"))
	if again, _ := s.Create(ctx, "feat_a_123abc"); len(again) != 1 {
		t.Fatalf("second Create = %v", again)
	}
	if kept, _ := os.ReadFile(filepath.Join(os.Getenv("LANES_DEMO_STATE"), "feat_a_123abc", "data.txt")); string(kept) != string(token) || len(token) == 0 {
		t.Fatalf("Create replaced the lane's data: %q then %q", token, kept)
	}
	if _, err := s.Create(ctx, "feat_b_456def"); err != nil {
		t.Fatal(err)
	}
	all, err := s.List(ctx)
	if err != nil || strings.Join(all, ",") != "feat_a_123abc,feat_b_456def" {
		t.Fatalf("List = %v, %v", all, err)
	}
	if owner, err := s.Owner(ctx, "feat_b_456def"); err != nil || owner != "feat_b_456def" {
		t.Fatalf("Owner = %q, %v", owner, err)
	}
	if err := s.Erase(ctx, "feat_b_456def", "feat_b_456def"); err != nil {
		t.Fatal(err)
	}
	if owner, _ := s.Owner(ctx, "feat_b_456def"); owner != "" {
		t.Fatalf("erased resource still owned by %q", owner)
	}
}

func TestEntryRebuildsTheAdapter(t *testing.T) {
	s := demoStore(t)
	en := s.Entry("feat_a_123abc", "feat_a_123abc")
	if en.Kind != KindScripts || en.Scripts == nil || en.Scripts.Erase != "tool/lane-store erase" {
		t.Fatalf("entry = %+v", en)
	}
	a, err := FromEntry(en, s.Env)
	if err != nil || a.Name() != "demo-store" {
		t.Fatalf("FromEntry = %v, %v", a, err)
	}
}

func TestScriptsFailureNamesTheVerb(t *testing.T) {
	s := &Scripts{Spec: config.Scripts{Name: "broken", Create: "echo nope >&2; exit 7"}, Env: Env{Dir: t.TempDir()}}
	_, err := s.Create(context.Background(), "lane")
	if err == nil || !strings.Contains(err.Error(), "broken: create failed") || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v", err)
	}
}

func TestFirebaseEraseStaysInTheWorktree(t *testing.T) {
	root := t.TempDir()
	f := &Firebase{Env: Env{Worktree: root}}
	ctx := context.Background()

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "x"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.Erase(ctx, "lane", outside); err == nil {
		t.Fatal("Erase outside the worktree returned no error")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside directory was removed: %v", err)
	}

	sibling := root + "-other"
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sibling) })
	if err := os.WriteFile(filepath.Join(sibling, "x"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.Erase(ctx, "lane", filepath.Join(sibling, "x")); err == nil {
		t.Fatal("Erase in a sibling prefix returned no error")
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Fatalf("sibling directory was removed: %v", err)
	}

	data := filepath.Join(root, ".emu-data")
	for _, p := range []string{data, data + ".next", data + ".old"} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "f"), []byte("f"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Erase(ctx, "lane", data); err != nil {
		t.Fatalf("Erase inside the worktree = %v", err)
	}
	for _, p := range []string{data, data + ".next", data + ".old"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s still exists after Erase: %v", p, err)
		}
	}
}

func TestFirebaseProcesses(t *testing.T) {
	f := &Firebase{
		Spec: config.Firebase{
			FunctionsTimeout: config.DefaultFirebaseFunctionsTimeout,
			ExportTimeout:    config.DefaultFirebaseExportTimeout,
		},
		Env: Env{Worktree: "/w"},
	}
	p, ok := f.Processes()[FirebasePiece]
	if !ok {
		t.Fatal("no firebase process")
	}
	cmd, ok := p["command"].(string)
	if !ok || !strings.HasSuffix(cmd, " firebase run") {
		t.Fatalf("command = %v", p["command"])
	}
	if wd, ok := p["working_dir"].(string); !ok || wd != "/w" {
		t.Fatalf("working_dir = %v", p["working_dir"])
	}
	shutdown, ok := p["shutdown"].(map[string]any)
	if !ok {
		t.Fatalf("shutdown = %v", p["shutdown"])
	}
	if v, ok := shutdown["timeout_seconds"].(int); !ok || v != 75 {
		t.Fatalf("shutdown timeout_seconds = %v", shutdown["timeout_seconds"])
	}
	if v, ok := shutdown["parent_only"].(bool); !ok || !v {
		t.Fatalf("shutdown parent_only = %v", shutdown["parent_only"])
	}
	if v, ok := shutdown["signal"].(int); !ok || v != 15 {
		t.Fatalf("shutdown signal = %v", shutdown["signal"])
	}
	probe, ok := p["readiness_probe"].(map[string]any)
	if !ok {
		t.Fatalf("readiness_probe = %v", p["readiness_probe"])
	}
	if v, ok := probe["failure_threshold"].(int); !ok || v != 480 {
		t.Fatalf("readiness failure_threshold = %v", probe["failure_threshold"])
	}
}

func TestFirebaseEntryRebuildsTheAdapter(t *testing.T) {
	f := &Firebase{Env: Env{Worktree: "/w"}}
	e := f.Entry("lane_1", "/w/.emu-data")
	if e.Kind != KindFirebase || e.Adapter != "firebase" || e.Resource != "/w/.emu-data" {
		t.Fatalf("entry = %+v", e)
	}
	a, err := FromEntry(e, Env{Worktree: "/w"})
	if err != nil || a.Kind() != KindFirebase || a.Name() != "firebase" {
		t.Fatalf("FromEntry = %v, %v", a, err)
	}
}

func TestFromConfigBuildsFirebase(t *testing.T) {
	cfg, err := config.Parse([]byte("schema: 1\nports: {block: 3, names: [firestore, firestore_ws, hub]}\nresources: [{firebase: {config: f.json}}]\n"))
	if err != nil {
		t.Fatal(err)
	}
	as := FromConfig(cfg, Env{Worktree: "/w"})
	if len(as) != 1 || as[0].Kind() != KindFirebase {
		t.Fatalf("FromConfig = %v", as)
	}
}
