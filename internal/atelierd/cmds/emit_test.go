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

	err := runEmit(t, "skill:activity", "cs-test",
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

	err := runEmit(t, "skill:activity", "cs-test", "--data-json", "usage=not-json")
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

	err := runEmit(t, "skill:activity", "cs-test",
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

func TestEmitSessionStartWithoutJSONLPathRegistersNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := runEmit(t, "hook:session-start", "cs-bare", "--data", "cwd=/tmp/repo"); err != nil {
		t.Fatalf("emit failed: %v", err)
	}
	if _, err := transcript.LoadState("cs-bare"); !os.IsNotExist(err) {
		t.Errorf("a session-start without jsonlPath must register nothing, got err=%v", err)
	}
	if env := readOnlyEnvelope(t, home); env.Type != "hook:session-start" {
		t.Errorf("envelope type = %q, want hook:session-start", env.Type)
	}
}

func TestEmitSessionStartSameTranscriptKeepsReadState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stale := time.Now().UTC().Add(-2 * time.Hour)
	existing := &transcript.State{
		ClaudeSessionID: "cs-resumed",
		JSONLPath:       "/tmp/cs-resumed.jsonl",
		Offsets:         map[string]int64{"cs-resumed.jsonl": 4242, "cs-resumed/subagents/agent-a.jsonl": 17},
		EmittedMinutes:  map[string]bool{"202610101003": true},
		LastTitle:       "Forge",
		LastTitleType:   "transcript:custom-title",
		LastActivityAt:  stale,
	}
	if err := transcript.SaveState(existing); err != nil {
		t.Fatalf("save existing state: %v", err)
	}

	if err := runEmit(t, "hook:session-start", "cs-resumed", "--data", "jsonlPath=/tmp/cs-resumed.jsonl"); err != nil {
		t.Fatalf("emit failed: %v", err)
	}
	s, err := transcript.LoadState("cs-resumed")
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if !reflect.DeepEqual(s.Offsets, existing.Offsets) || !s.EmittedMinutes["202610101003"] || s.LastTitle != "Forge" {
		t.Errorf("resume reset the read state: %+v", s)
	}
	if !s.LastActivityAt.After(stale) {
		t.Errorf("LastActivityAt = %v, want refreshed past %v", s.LastActivityAt, stale)
	}
}

func TestEmitSessionStartOtherTranscriptResetsOffsetsKeepsMinutes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := transcript.SaveState(&transcript.State{
		ClaudeSessionID: "cs-moved",
		JSONLPath:       "/old/cs-moved.jsonl",
		Offsets:         map[string]int64{"cs-moved.jsonl": 4242},
		EmittedMinutes:  map[string]bool{"202610101003": true},
	}); err != nil {
		t.Fatalf("save existing state: %v", err)
	}

	if err := runEmit(t, "hook:session-start", "cs-moved", "--data", "jsonlPath=/new/cs-moved.jsonl"); err != nil {
		t.Fatalf("emit failed: %v", err)
	}
	s, err := transcript.LoadState("cs-moved")
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if s.JSONLPath != "/new/cs-moved.jsonl" || len(s.Offsets) != 0 {
		t.Errorf("state = path %q offsets %v, want the new path and no offsets", s.JSONLPath, s.Offsets)
	}
	if !s.EmittedMinutes["202610101003"] {
		t.Error("emitted minutes lost on a transcript change")
	}
}

func TestEmitSessionStartReplacesUnreadableState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeJSONL(t, transcript.SessionFile("cs-corrupt"), `{not json`)

	if err := runEmit(t, "hook:session-start", "cs-corrupt", "--data", "jsonlPath=/tmp/cs-corrupt.jsonl"); err != nil {
		t.Fatalf("emit failed: %v", err)
	}
	if s, err := transcript.LoadState("cs-corrupt"); err != nil || s.JSONLPath != "/tmp/cs-corrupt.jsonl" {
		t.Fatalf("state = %v, %v; want a fresh registration", s, err)
	}
}

func TestEmitRefusesRetiredAndReaderOnlyTypes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	for _, typ := range []string{
		"hook:stop",
		"hook:user-prompt-submit",
		"hook:pre-tool-use",
		"hook:post-tool-use",
		"hook:assistant-turn",
		"activity:minute",
	} {
		if err := runEmit(t, typ, "s1"); err == nil {
			t.Errorf("emit %s succeeded, want an error", typ)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(home, ".atelier", "outbox"))
	if len(entries) != 0 {
		t.Errorf("outbox holds %d files after refused emits, want 0", len(entries))
	}
}

func TestEmitSessionEndLeavesReaderStateAlone(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := transcript.SaveState(&transcript.State{
		ClaudeSessionID: "cs-watched",
		JSONLPath:       "/tmp/cs-watched.jsonl",
		Offsets:         map[string]int64{"cs-watched.jsonl": 7},
		LastActivityAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save state: %v", err)
	}

	if err := runEmit(t, "hook:session-end", "cs-watched"); err != nil {
		t.Fatalf("emit failed: %v", err)
	}
	if _, err := transcript.LoadState("cs-watched"); err != nil {
		t.Errorf("reader state must survive session-end (offsets guard re-emission): %v", err)
	}
}
