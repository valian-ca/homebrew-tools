package transcript

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveLoadState_ParentRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	in := &State{
		ClaudeSessionID: "cs-parent",
		JSONLPath:       "/tmp/cs-parent.jsonl",
		Offset:          42,
		LastMsgID:       "msg_A",
		LastActivityAt:  time.Now().UTC().Truncate(time.Second),
	}
	if err := SaveState(in); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	want := filepath.Join(SessionsDir(), "cs-parent.json")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("parent state file missing at %s: %v", want, err)
	}

	got, err := LoadState("cs-parent")
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got.ClaudeSessionID != in.ClaudeSessionID || got.Offset != in.Offset || got.LastMsgID != in.LastMsgID {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, in)
	}
	if got.WatcherKey != "" {
		t.Errorf("parent state must serialize WatcherKey as empty (omitempty), got %q", got.WatcherKey)
	}
	if got.Key() != "cs-parent" {
		t.Errorf("Key() on parent = %q, want cs-parent", got.Key())
	}
	if got.IsSubagent() {
		t.Errorf("parent state should not report IsSubagent")
	}
}

func TestSaveLoadState_SubagentRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	watcherKey := filepath.Join("cs-parent", "subagents", "agent-abc")
	in := &State{
		ClaudeSessionID: "cs-parent",
		WatcherKey:      watcherKey,
		JSONLPath:       "/tmp/cs-parent/subagents/agent-abc.jsonl",
		Offset:          7,
		LastMsgID:       "msg_haiku",
	}
	if err := SaveState(in); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	want := filepath.Join(SessionsDir(), "cs-parent", "subagents", "agent-abc.json")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("subagent state file missing at %s: %v", want, err)
	}

	got, err := LoadState(watcherKey)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got.ClaudeSessionID != "cs-parent" {
		t.Errorf("ClaudeSessionID = %q, want cs-parent (parent's id, not synthetic)", got.ClaudeSessionID)
	}
	if got.WatcherKey != watcherKey {
		t.Errorf("WatcherKey = %q, want %q", got.WatcherKey, watcherKey)
	}
	if got.Key() != watcherKey {
		t.Errorf("Key() on subagent = %q, want %q", got.Key(), watcherKey)
	}
	if !got.IsSubagent() {
		t.Errorf("subagent state must report IsSubagent")
	}
}

func TestLoadState_AbsentReturnsErrNotExist(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := LoadState("never-saved")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("LoadState on absent state: want ErrNotExist, got %v", err)
	}
}

func TestSaveState_RejectsEmptyClaudeSessionID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	err := SaveState(&State{WatcherKey: "x/subagents/y", JSONLPath: "/tmp/x.jsonl"})
	if err == nil {
		t.Fatal("SaveState with empty ClaudeSessionID should fail")
	}
}

func TestListStates_ReturnsParentsAndSubagents(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	parent := &State{
		ClaudeSessionID: "cs-parent",
		JSONLPath:       "/tmp/cs-parent.jsonl",
	}
	subA := &State{
		ClaudeSessionID: "cs-parent",
		WatcherKey:      filepath.Join("cs-parent", "subagents", "agent-1"),
		JSONLPath:       "/tmp/cs-parent/subagents/agent-1.jsonl",
	}
	subB := &State{
		ClaudeSessionID: "cs-parent",
		WatcherKey:      filepath.Join("cs-parent", "subagents", "agent-2"),
		JSONLPath:       "/tmp/cs-parent/subagents/agent-2.jsonl",
	}
	other := &State{
		ClaudeSessionID: "cs-other",
		JSONLPath:       "/tmp/cs-other.jsonl",
	}
	for _, s := range []*State{parent, subA, subB, other} {
		if err := SaveState(s); err != nil {
			t.Fatalf("SaveState %s: %v", s.Key(), err)
		}
	}

	got, err := ListStates()
	if err != nil {
		t.Fatalf("ListStates: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("ListStates returned %d, want 4", len(got))
	}

	wantKeys := map[string]bool{
		"cs-other":  false,
		"cs-parent": false,
		filepath.Join("cs-parent", "subagents", "agent-1"): false,
		filepath.Join("cs-parent", "subagents", "agent-2"): false,
	}
	for _, s := range got {
		k := s.Key()
		if _, ok := wantKeys[k]; !ok {
			t.Errorf("unexpected key in ListStates: %q", k)
			continue
		}
		wantKeys[k] = true
	}
	for k, found := range wantKeys {
		if !found {
			t.Errorf("ListStates missing key %q", k)
		}
	}

	for _, s := range got {
		if s.Key() == "cs-parent" || s.Key() == "cs-other" {
			if s.WatcherKey != "" {
				t.Errorf("parent %q has WatcherKey %q, want empty", s.Key(), s.WatcherKey)
			}
			if s.IsSubagent() {
				t.Errorf("parent %q reports IsSubagent", s.Key())
			}
			continue
		}
		if !s.IsSubagent() {
			t.Errorf("subagent %q does not report IsSubagent", s.Key())
		}
		if s.ClaudeSessionID != "cs-parent" {
			t.Errorf("subagent %q ClaudeSessionID = %q, want cs-parent", s.Key(), s.ClaudeSessionID)
		}
	}
}

func TestListStates_AbsentDirReturnsNilNoError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got, err := ListStates()
	if err != nil {
		t.Fatalf("ListStates on missing sessions dir: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListStates on missing dir: want 0, got %d", len(got))
	}
}

func TestListStates_IgnoresUnexpectedDepths(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	parent := &State{ClaudeSessionID: "cs-parent", JSONLPath: "/tmp/p.jsonl"}
	if err := SaveState(parent); err != nil {
		t.Fatalf("SaveState parent: %v", err)
	}

	stray := filepath.Join(SessionsDir(), "cs-parent", "stray.json")
	if err := os.MkdirAll(filepath.Dir(stray), 0o700); err != nil {
		t.Fatalf("mkdir stray: %v", err)
	}
	if err := os.WriteFile(stray, []byte(`{"claudeSessionId":"x","jsonlPath":"/tmp/x.jsonl"}`), 0o600); err != nil {
		t.Fatalf("write stray: %v", err)
	}

	deep := filepath.Join(SessionsDir(), "cs-parent", "subagents", "nested", "deep.json")
	if err := os.MkdirAll(filepath.Dir(deep), 0o700); err != nil {
		t.Fatalf("mkdir deep: %v", err)
	}
	if err := os.WriteFile(deep, []byte(`{"claudeSessionId":"y","jsonlPath":"/tmp/y.jsonl"}`), 0o600); err != nil {
		t.Fatalf("write deep: %v", err)
	}

	got, err := ListStates()
	if err != nil {
		t.Fatalf("ListStates: %v", err)
	}
	if len(got) != 1 || got[0].Key() != "cs-parent" {
		t.Fatalf("ListStates should yield only cs-parent, got %+v", keys(got))
	}
}

func keys(states []*State) []string {
	ks := make([]string, len(states))
	for i, s := range states {
		ks[i] = s.Key()
	}
	return ks
}
