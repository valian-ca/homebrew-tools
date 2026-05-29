package sessionstore

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func writeStoreFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestScanEntries_NestedStore(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "acct-1", "ws-1")
	b := filepath.Join(root, "acct-1", "ws-2")
	writeStoreFile(t, a, "local_one.json", `{"cliSessionId":"cs-a","title":"First","titleSource":"auto"}`)
	writeStoreFile(t, b, "local_two.json", `{"cliSessionId":"cs-b","title":"Second","titleSource":"user"}`)

	entries, err := ScanEntries(root)
	if err != nil {
		t.Fatalf("ScanEntries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(entries))
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].CliSessionID < entries[j].CliSessionID })
	if entries[0].CliSessionID != "cs-a" || entries[0].Title != "First" || entries[0].TitleSource != "auto" {
		t.Errorf("entry 0 = %+v", entries[0])
	}
	if entries[1].CliSessionID != "cs-b" || entries[1].Title != "Second" || entries[1].TitleSource != "user" {
		t.Errorf("entry 1 = %+v", entries[1])
	}
}

func TestScanEntries_SkipsNonLocalAndUnparseable(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "acct", "ws")
	writeStoreFile(t, dir, "local_good.json", `{"cliSessionId":"cs-ok","title":"Ok","titleSource":"auto"}`)
	writeStoreFile(t, dir, "other.json", `{"cliSessionId":"cs-other","title":"X"}`)
	writeStoreFile(t, dir, "local_broken.json", `{not json`)
	writeStoreFile(t, dir, "local_noid.json", `{"title":"orphan","titleSource":"auto"}`)

	entries, err := ScanEntries(root)
	if err != nil {
		t.Fatalf("ScanEntries: %v", err)
	}
	if len(entries) != 1 || entries[0].CliSessionID != "cs-ok" {
		t.Fatalf("want only cs-ok, got %+v", entries)
	}
}

func TestScanEntries_MissingRootIsEmpty(t *testing.T) {
	entries, err := ScanEntries(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("ScanEntries on missing root: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("want no entries, got %d", len(entries))
	}
}
