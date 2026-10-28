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
