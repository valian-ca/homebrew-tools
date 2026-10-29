package main

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/valian-ca/homebrew-tools/internal/lanes/config"
)

const demo = "../../examples/lanes-demo/"

func fences(t *testing.T, lang string) []string {
	t.Helper()
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile("(?s)```" + lang + "\n(.*?)```")
	var out []string
	for _, m := range re.FindAllStringSubmatch(string(readme), -1) {
		out = append(out, m[1])
	}
	return out
}

type settings struct {
	Hooks map[string][]struct {
		Matcher string `json:"matcher"`
		Hooks   []struct {
			Command string `json:"command"`
			Timeout int    `json:"timeout"`
		} `json:"hooks"`
	} `json:"hooks"`
}

func TestDemoFollowsContract(t *testing.T) {
	data, err := os.ReadFile(demo + "lanes.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(data)
	if err != nil {
		t.Fatalf("the demo lanes.yaml breaks schema 1: %v", err)
	}
	if cfg.Schema != 1 || len(cfg.Resources) == 0 || cfg.Resources[0].Scripts == nil || len(cfg.Setup) == 0 || len(cfg.Shared) == 0 || len(cfg.Generate) == 0 {
		t.Fatalf("the demo no longer exercises every part of the contract: %+v", cfg)
	}

	jsonFences := fences(t, "json")
	if len(jsonFences) != 1 {
		t.Fatalf("README has %d json fences, want the hooks one", len(jsonFences))
	}
	var want, got settings
	if err := json.Unmarshal([]byte(jsonFences[0]), &want); err != nil {
		t.Fatalf("README hooks: %v", err)
	}
	raw, err := os.ReadFile(demo + ".claude/settings.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{"SessionStart", "CwdChanged", "PostToolUse", "SessionEnd"} {
		w, g := want.Hooks[event], got.Hooks[event]
		if len(w) != 1 || len(g) != 1 || w[0].Matcher != g[0].Matcher || w[0].Hooks[0] != g[0].Hooks[0] {
			t.Errorf("%s: demo %+v, contract %+v", event, g, w)
		}
	}

	envrc, err := os.ReadFile(demo + ".envrc")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range fences(t, "sh") {
		if strings.Contains(f, strings.TrimSpace(string(envrc))) {
			found = true
		}
	}
	if !found {
		t.Errorf("the demo .envrc %q is not the contract's form", envrc)
	}
}
