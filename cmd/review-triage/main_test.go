package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunVersion(t *testing.T) {
	if got := run([]string{"--version"}); got != exitOK {
		t.Fatalf("--version => %d, want %d", got, exitOK)
	}
}

func TestRunMissingFlags(t *testing.T) {
	if got := run([]string{"--input", "x.json"}); got != exitInternal {
		t.Fatalf("missing --output => %d, want %d", got, exitInternal)
	}
	if got := run(nil); got != exitInternal {
		t.Fatalf("no flags => %d, want %d", got, exitInternal)
	}
}

func TestRunUnreadableInput(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out.json")
	if got := run([]string{"--input", "/no/such/file.json", "--output", out}); got != exitInternal {
		t.Fatalf("missing input => %d, want %d", got, exitInternal)
	}
}

func TestRunSchemaMismatch(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.json")
	if err := os.WriteFile(in, []byte(`{"schemaVersion":2,"findings":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := run([]string{"--input", in, "--output", filepath.Join(dir, "out.json")}); got != exitInternal {
		t.Fatalf("schema mismatch => %d, want %d", got, exitInternal)
	}
}

func TestRunEmptyFindingsWritesOutput(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.json")
	if err := os.WriteFile(in, []byte(`{"schemaVersion":1,"findings":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.json")
	if got := run([]string{"--input", in, "--output", out}); got != exitOK {
		t.Fatalf("empty findings => %d, want %d", got, exitOK)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("output not written: %v", err)
	}
	if !strings.Contains(string(data), `"decisions"`) {
		t.Fatalf("output missing decisions: %s", data)
	}
}
