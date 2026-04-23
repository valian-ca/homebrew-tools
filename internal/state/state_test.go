package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingFile(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s != (State{}) {
		t.Fatalf("expected zero State, got %+v", s)
	}
}

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "frn.json")
	in := State{
		Flavor:        "staging",
		DeviceID:      "ABCD",
		DeviceLabel:   "Pixel 7",
		VMServiceFile: ".dart_tool/valian/vmservice.uri",
	}
	if err := Save(path, in); err != nil {
		t.Fatalf("save: %v", err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !out.HasFlavor {
		t.Fatal("HasFlavor should be true after round-trip")
	}
	out.HasFlavor = false
	if out != in {
		t.Fatalf("mismatch: got %+v, want %+v", out, in)
	}
}

func TestHasFlavorEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "frn.json")
	// Explicit empty flavor — user chose --no-flavor.
	if err := os.WriteFile(path, []byte(`{"flavor":"","device_id":"X"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !s.HasFlavor {
		t.Fatal("HasFlavor should be true for empty flavor")
	}
	if s.Flavor != "" {
		t.Fatalf("expected empty flavor, got %q", s.Flavor)
	}
}

func TestHasFlavorAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "frn.json")
	if err := os.WriteFile(path, []byte(`{"device_id":"X"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.HasFlavor {
		t.Fatal("HasFlavor should be false when flavor key is absent")
	}
}
