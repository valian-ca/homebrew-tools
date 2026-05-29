package cmds

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fsnotify/fsnotify"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/outbox"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/sessionstore"
)

func TestShouldHandleStoreEvent(t *testing.T) {
	cases := []struct {
		name string
		ev   fsnotify.Event
		want bool
	}{
		{"local json write", fsnotify.Event{Name: filepath.Join("a", "b", "local_x.json"), Op: fsnotify.Write}, true},
		{"local json create", fsnotify.Event{Name: filepath.Join("a", "b", "local_x.json"), Op: fsnotify.Create}, true},
		{"tmp ignored", fsnotify.Event{Name: filepath.Join("a", "b", "local_x.json.tmp"), Op: fsnotify.Write}, false},
		{"non-local json ignored", fsnotify.Event{Name: filepath.Join("a", "b", "other.json"), Op: fsnotify.Write}, false},
		{"dir create passes through", fsnotify.Event{Name: filepath.Join("a", "newsub"), Op: fsnotify.Create}, true},
		{"dir write rejected", fsnotify.Event{Name: filepath.Join("a", "newsub"), Op: fsnotify.Write}, false},
		{"chmod rejected", fsnotify.Event{Name: filepath.Join("a", "b", "local_x.json"), Op: fsnotify.Chmod}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldHandleStoreEvent(tc.ev); got != tc.want {
				t.Errorf("shouldHandleStoreEvent(%+v) = %v, want %v", tc.ev, got, tc.want)
			}
		})
	}
}

func TestConsumeStoreEntry_CorruptStateSkips(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := filepath.Join(os.Getenv("HOME"), ".atelier", "session-titles")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cs-bad.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	consumeStoreEntry(sessionstore.Entry{CliSessionID: "cs-bad", Title: "X", TitleSource: "auto"})
	if n := countOutbox(t); n != 0 {
		t.Fatalf("a corrupt state file should skip the session, got %d", n)
	}
}

func countOutbox(t *testing.T) int {
	t.Helper()
	files, err := outbox.List()
	if err != nil {
		t.Fatalf("outbox.List: %v", err)
	}
	return len(files)
}

func TestConsumeStoreEntry_EmitsThenDedups(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	entry := sessionstore.Entry{CliSessionID: "cs-1", Title: "Hello", TitleSource: "auto"}
	consumeStoreEntry(entry)
	if n := countOutbox(t); n != 1 {
		t.Fatalf("first sight should emit one envelope, got %d", n)
	}

	consumeStoreEntry(entry)
	if n := countOutbox(t); n != 1 {
		t.Fatalf("unchanged title should not emit again, got %d", n)
	}

	consumeStoreEntry(sessionstore.Entry{CliSessionID: "cs-1", Title: "World", TitleSource: "auto"})
	if n := countOutbox(t); n != 2 {
		t.Fatalf("changed title should emit a second envelope, got %d", n)
	}
}
