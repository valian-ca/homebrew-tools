package cmds

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/outbox"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/transcript"
)

func runEmit(t *testing.T, args ...string) error {
	t.Helper()
	c := NewEmitCmd()
	c.SetArgs(args)
	c.SilenceUsage = true
	c.SilenceErrors = true
	return c.Execute()
}

func readOnlyEnvelope(t *testing.T, home string) *outbox.Envelope {
	t.Helper()
	dir := filepath.Join(home, ".atelier", "outbox")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read outbox dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("outbox holds %d files, want 1", len(entries))
	}
	bytes, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read envelope: %v", err)
	}
	var env outbox.Envelope
	if err := json.Unmarshal(bytes, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	return &env
}

func TestEmitDataJSONWritesTypedPayload(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	err := runEmit(t, "hook:assistant-turn", "cs-test",
		"--data", "model=gpt-5.6-sol",
		"--data-json", `usage={"input_tokens":1200,"cache_creation":{"ephemeral_5m_input_tokens":800}}`)
	if err != nil {
		t.Fatalf("emit failed: %v", err)
	}

	env := readOnlyEnvelope(t, home)
	want := map[string]any{
		"model": "gpt-5.6-sol",
		"usage": map[string]any{
			"input_tokens":   float64(1200),
			"cache_creation": map[string]any{"ephemeral_5m_input_tokens": float64(800)},
		},
	}
	if !reflect.DeepEqual(env.Payload, want) {
		t.Errorf("payload = %v, want %v", env.Payload, want)
	}
}

func TestEmitDataJSONInvalidJSONFailsWithoutWriting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	err := runEmit(t, "hook:assistant-turn", "cs-test", "--data-json", "usage=not-json")
	if err == nil {
		t.Fatal("emit accepted invalid JSON, want error")
	}
	if _, statErr := os.Stat(filepath.Join(home, ".atelier", "outbox")); !os.IsNotExist(statErr) {
		entries, _ := os.ReadDir(filepath.Join(home, ".atelier", "outbox"))
		if len(entries) != 0 {
			t.Errorf("outbox holds %d files after failed emit, want 0", len(entries))
		}
	}
}

func TestEmitDataJSONWinsOverDataOnSameKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	err := runEmit(t, "hook:assistant-turn", "cs-test",
		"--data", "count=verbatim",
		"--data-json", "count=42")
	if err != nil {
		t.Fatalf("emit failed: %v", err)
	}

	env := readOnlyEnvelope(t, home)
	if got, want := env.Payload["count"], float64(42); !reflect.DeepEqual(got, want) {
		t.Errorf("payload[count] = %v (%T), want %v", got, got, want)
	}
}

func TestEmitSessionStartNonStringJSONLPathFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	err := runEmit(t, "hook:session-start", "cs-test", "--data-json", "jsonlPath=123")
	if err == nil {
		t.Fatal("emit accepted a non-string jsonlPath, want error")
	}
	if _, lerr := transcript.LoadState("cs-test"); !os.IsNotExist(lerr) {
		t.Errorf("no state should be registered on a rejected jsonlPath, got err=%v", lerr)
	}
	entries, _ := os.ReadDir(filepath.Join(home, ".atelier", "outbox"))
	if len(entries) != 0 {
		t.Errorf("outbox holds %d files after failed emit, want 0", len(entries))
	}
}

func TestEmitSessionStartEmptyJSONLPathFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := runEmit(t, "hook:session-start", "cs-test", "--data", "jsonlPath="); err == nil {
		t.Fatal("emit accepted an empty jsonlPath, want error")
	}
}

func TestEmitSessionStartWithoutJSONLPathRegistersTranscriptLessState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := runEmit(t, "hook:session-start", "cs-opencode", "--data", "cwd=/tmp/repo"); err != nil {
		t.Fatalf("emit failed: %v", err)
	}
	s, err := transcript.LoadState("cs-opencode")
	if err != nil {
		t.Fatalf("transcript-less session was not registered: %v", err)
	}
	if s.JSONLPath != "" {
		t.Errorf("JSONLPath = %q, want empty", s.JSONLPath)
	}
	if s.LastActivityAt.IsZero() {
		t.Error("LastActivityAt not stamped")
	}
}

func TestEmitSessionStartWithoutJSONLPathKeepsExistingState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	existing := &transcript.State{
		ClaudeSessionID: "cs-reborn",
		JSONLPath:       "/tmp/cs-reborn.jsonl",
		Offset:          4242,
		LastActivityAt:  time.Now().UTC(),
	}
	if err := transcript.SaveState(existing); err != nil {
		t.Fatalf("save existing state: %v", err)
	}

	if err := runEmit(t, "hook:session-start", "cs-reborn"); err != nil {
		t.Fatalf("emit failed: %v", err)
	}
	s, err := transcript.LoadState("cs-reborn")
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if s.JSONLPath != existing.JSONLPath || s.Offset != existing.Offset {
		t.Errorf("existing watcher state was overwritten: got path=%q offset=%d", s.JSONLPath, s.Offset)
	}
}

func TestEmitRefreshesTranscriptLessActivityClock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	stale := time.Now().UTC().Add(-2 * time.Hour)
	if err := transcript.SaveState(&transcript.State{
		ClaudeSessionID: "cs-opencode",
		LastActivityAt:  stale,
	}); err != nil {
		t.Fatalf("save state: %v", err)
	}

	if err := runEmit(t, "hook:stop", "cs-opencode"); err != nil {
		t.Fatalf("emit failed: %v", err)
	}
	s, err := transcript.LoadState("cs-opencode")
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if !s.LastActivityAt.After(stale) {
		t.Errorf("LastActivityAt = %v, want refreshed past %v", s.LastActivityAt, stale)
	}
}

func TestEmitSessionEndRetiresTranscriptLessState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := transcript.SaveState(&transcript.State{
		ClaudeSessionID: "cs-opencode",
		LastActivityAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save state: %v", err)
	}

	if err := runEmit(t, "hook:session-end", "cs-opencode"); err != nil {
		t.Fatalf("emit failed: %v", err)
	}
	if _, err := transcript.LoadState("cs-opencode"); !os.IsNotExist(err) {
		t.Errorf("transcript-less state should be retired on session-end, got err=%v", err)
	}
}

func TestEmitSessionEndLeavesWatcherStateAlone(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := transcript.SaveState(&transcript.State{
		ClaudeSessionID: "cs-watched",
		JSONLPath:       "/tmp/cs-watched.jsonl",
		Offset:          7,
		LastActivityAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save state: %v", err)
	}

	if err := runEmit(t, "hook:session-end", "cs-watched"); err != nil {
		t.Fatalf("emit failed: %v", err)
	}
	if _, err := transcript.LoadState("cs-watched"); err != nil {
		t.Errorf("watcher-backed state must survive session-end (offset guards dedup): %v", err)
	}
}
