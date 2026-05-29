package outbox

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCountSeparatesActiveCorruptAndRejected verifies that the active queue
// (Count, *.json) and the quarantine gauge (CountRejected, *.json.rejected) are
// disjoint, and that .corrupt files count toward neither. This is what lets a
// quarantined event leave the active queue so the backlog drains.
func TestCountSeparatesActiveCorruptAndRejected(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	outboxDir := filepath.Join(home, ".atelier", "outbox")
	if err := os.MkdirAll(outboxDir, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(outboxDir, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("01AAAAAAAAAAAAAAAAAAAAAAAA.json")
	write("01BBBBBBBBBBBBBBBBBBBBBBBB.json")
	write("01CCCCCCCCCCCCCCCCCCCCCCCC.json.rejected")
	write("01DDDDDDDDDDDDDDDDDDDDDDDD.json.corrupt")

	active, err := Count()
	if err != nil {
		t.Fatal(err)
	}
	if active != 2 {
		t.Fatalf("Count() = %d, want 2 (only *.json)", active)
	}

	rejected, err := CountRejected()
	if err != nil {
		t.Fatal(err)
	}
	if rejected != 1 {
		t.Fatalf("CountRejected() = %d, want 1 (only *.json.rejected)", rejected)
	}
}

// TestCountRejectedMissingDir returns zero (not an error) when the outbox has
// never been created — same tolerance as Count.
func TestCountRejectedMissingDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	n, err := CountRejected()
	if err != nil {
		t.Fatalf("CountRejected() on missing dir: unexpected error %v", err)
	}
	if n != 0 {
		t.Fatalf("CountRejected() on missing dir = %d, want 0", n)
	}
}
