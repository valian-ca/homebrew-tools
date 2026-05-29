package sessionstore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveLoadState_RoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	in := &State{
		CliSessionID:    "cs-round",
		LastTitle:       "A title",
		LastTitleSource: "user",
		LastActivityAt:  time.Now().UTC().Truncate(time.Second),
	}
	if err := SaveState(in); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	got, err := LoadState("cs-round")
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got.CliSessionID != in.CliSessionID || got.LastTitle != in.LastTitle || got.LastTitleSource != in.LastTitleSource {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, in)
	}
}

func TestLoadState_AbsentReturnsErrNotExist(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := LoadState("never-saved")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("LoadState on absent state: want ErrNotExist, got %v", err)
	}
}

func TestSaveState_RejectsEmptyID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := SaveState(&State{LastTitle: "x"}); err == nil {
		t.Fatal("SaveState with empty CliSessionID should fail")
	}
}

func TestState_RejectsPathSeparators(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, bad := range []string{"../escape", "a/b", `a\b`, ".."} {
		if err := SaveState(&State{CliSessionID: bad, LastTitle: "x"}); err == nil {
			t.Errorf("SaveState(%q) should reject unsafe id", bad)
		}
		if _, err := LoadState(bad); err == nil {
			t.Errorf("LoadState(%q) should reject unsafe id", bad)
		}
	}
}

func TestStateFile_UnderSessionTitlesDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := SaveState(&State{CliSessionID: "cs-path", LastTitle: "x", LastTitleSource: "auto"}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".atelier", "session-titles", "cs-path.json")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("state file missing at %s: %v", want, err)
	}
}
