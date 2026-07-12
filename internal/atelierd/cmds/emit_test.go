package cmds

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/outbox"
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
