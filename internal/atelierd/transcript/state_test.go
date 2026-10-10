package transcript

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestSaveLoadState_RoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	at := time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC)
	in := &State{
		ClaudeSessionID: "cs-1",
		JSONLPath:       "/p/cs-1.jsonl",
		Offsets:         map[string]int64{"cs-1.jsonl": 42, "cs-1/subagents/workflows/wf_a/agent-b.jsonl": 7},
		EmittedMinutes:  map[string]bool{"202610101000": true},
		LastTitle:       "T",
		LastTitleType:   "transcript:custom-title",
		LastActivityAt:  at,
	}
	if err := SaveState(in); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	out, err := LoadState("cs-1")
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if out.SchemaVersion != SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", out.SchemaVersion, SchemaVersion)
	}
	if out.Offsets["cs-1.jsonl"] != 42 || out.Offsets["cs-1/subagents/workflows/wf_a/agent-b.jsonl"] != 7 {
		t.Errorf("Offsets = %v", out.Offsets)
	}
	if !out.EmittedMinutes["202610101000"] || out.LastTitle != "T" || !out.LastActivityAt.Equal(at) {
		t.Errorf("state = %+v", out)
	}
}

func TestLoadState_AbsentReturnsErrNotExist(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := LoadState("cs-none"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want ErrNotExist", err)
	}
}

func TestSaveState_RejectsUnsafeIDs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, id := range []string{"", ".", "..", "a/b", `a\b`, "a:b", "a\x00b"} {
		if err := SaveState(&State{ClaudeSessionID: id, JSONLPath: "/p.jsonl"}); err == nil {
			t.Errorf("SaveState(%q) accepted an unsafe id", id)
		}
	}
}

func TestSaveState_PrunesMinutesOlderThanADay(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := &State{ClaudeSessionID: "cs-1", JSONLPath: "/p/cs-1.jsonl", EmittedMinutes: map[string]bool{
		"202610091000": true,
		"202610091001": true,
		"202610101001": true,
	}}
	if err := SaveState(s); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	out, _ := LoadState("cs-1")
	var keys []string
	for k := range out.EmittedMinutes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) != 2 || keys[0] != "202610091001" || keys[1] != "202610101001" {
		t.Fatalf("EmittedMinutes = %v, want the last 24 h only", keys)
	}
}

func TestTreeFiles_ParentAndAgentsAtAnyDepth(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, "cs-1.jsonl")
	writeFile(t, parent, "")
	sub := filepath.Join(dir, "cs-1", "subagents")
	for _, f := range []string{
		"agent-a.jsonl",
		"agent-a.meta.json",
		"agent-a.forked-skill.json",
		"workflows/wf_1/agent-b.jsonl",
		"workflows/wf_1/journal.jsonl",
		"workflows/wf_1/deeper/agent-c.jsonl",
		"notes.jsonl",
	} {
		writeFile(t, filepath.Join(sub, f), "")
	}
	s := &State{ClaudeSessionID: "cs-1", JSONLPath: parent}

	var keys []string
	for _, f := range s.TreeFiles() {
		keys = append(keys, s.OffsetKey(f))
	}
	sort.Strings(keys)
	want := []string{
		"cs-1.jsonl",
		"cs-1/subagents/agent-a.jsonl",
		"cs-1/subagents/workflows/wf_1/agent-b.jsonl",
		"cs-1/subagents/workflows/wf_1/deeper/agent-c.jsonl",
	}
	if len(keys) != len(want) {
		t.Fatalf("TreeFiles keys = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("TreeFiles keys = %v, want %v", keys, want)
		}
	}
}

func TestTreeFiles_NoSubagentsDir(t *testing.T) {
	s := &State{ClaudeSessionID: "cs-1", JSONLPath: filepath.Join(t.TempDir(), "cs-1.jsonl")}
	if files := s.TreeFiles(); len(files) != 1 || files[0] != s.JSONLPath {
		t.Fatalf("TreeFiles = %v, want the parent only", files)
	}
}

// A 0.18.1 tree: parent state + per-subagent state files, offsets at EOF.
func writeLegacyTree(t *testing.T, transcripts string) {
	t.Helper()
	sessions := SessionsDir()
	parent := filepath.Join(transcripts, "cs-old.jsonl")
	writeFile(t, filepath.Join(sessions, "cs-old.json"),
		`{"claudeSessionId":"cs-old","jsonlPath":"`+parent+`","offset":1200,"lastMsgId":"m","closedToolUseIds":{"t":true},"lastTitle":"Forge","lastTitleType":"transcript:custom-title","lastActivityAt":"2026-10-09T10:00:00Z"}`)
	writeFile(t, filepath.Join(sessions, "cs-old", "subagents", "agent-a.json"),
		`{"claudeSessionId":"cs-old","watcherKey":"cs-old/subagents/agent-a","jsonlPath":"`+filepath.Join(transcripts, "cs-old", "subagents", "agent-a.jsonl")+`","offset":300,"lastActivityAt":"2026-10-09T10:00:00Z"}`)
}

func TestLoadState_MigratesLegacyTreeKeepingOffsets(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	transcripts := t.TempDir()
	writeLegacyTree(t, transcripts)

	s, err := LoadState("cs-old")
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if s.SchemaVersion != SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", s.SchemaVersion, SchemaVersion)
	}
	if s.Offsets["cs-old.jsonl"] != 1200 || s.Offsets["cs-old/subagents/agent-a.jsonl"] != 300 {
		t.Errorf("Offsets = %v, want parent 1200 and agent-a 300", s.Offsets)
	}
	if s.LastTitle != "Forge" || s.LastTitleType != "transcript:custom-title" {
		t.Errorf("title keys lost: %q / %q", s.LastTitle, s.LastTitleType)
	}
	if !s.LastActivityAt.Equal(time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("LastActivityAt = %s", s.LastActivityAt)
	}
	if _, err := os.Stat(filepath.Join(SessionsDir(), "cs-old")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("legacy subagent state dir still present: %v", err)
	}
	again, err := LoadState("cs-old")
	if err != nil || again.Offsets["cs-old/subagents/agent-a.jsonl"] != 300 {
		t.Fatalf("reload after migration: %v, %v", err, again)
	}
}

func TestLoadState_DeletesLegacyTranscriptLessState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeFile(t, SessionFile("cs-opencode"), `{"claudeSessionId":"cs-opencode","jsonlPath":"","offset":0,"lastActivityAt":"2026-10-09T10:00:00Z"}`)

	if _, err := LoadState("cs-opencode"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want ErrNotExist", err)
	}
	if _, err := os.Stat(SessionFile("cs-opencode")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("transcript-less state still on disk: %v", err)
	}
}

func TestListStates_TopLevelOnlyAndMigrates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	transcripts := t.TempDir()
	writeLegacyTree(t, transcripts)
	if err := SaveState(&State{ClaudeSessionID: "cs-new", JSONLPath: "/p/cs-new.jsonl"}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	writeFile(t, SessionFile("cs-opencode"), `{"claudeSessionId":"cs-opencode","jsonlPath":""}`)
	writeFile(t, SessionFile("cs-broken"), `{not json`)

	states, err := ListStates()
	if err != nil {
		t.Fatalf("ListStates: %v", err)
	}
	if len(states) != 2 || states[0].ClaudeSessionID != "cs-new" || states[1].ClaudeSessionID != "cs-old" {
		t.Fatalf("ListStates = %v, want [cs-new cs-old]", states)
	}
	if states[1].Offsets["cs-old/subagents/agent-a.jsonl"] != 300 {
		t.Errorf("listed legacy state not migrated: %v", states[1].Offsets)
	}
}

func TestListStates_AbsentDirReturnsNil(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	states, err := ListStates()
	if err != nil || states != nil {
		t.Fatalf("ListStates = %v, %v; want nil, nil", states, err)
	}
}

func TestDeleteState_RemovesFileAndLegacyDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeFile(t, SessionFile("cs-1"), `{}`)
	writeFile(t, filepath.Join(SessionsDir(), "cs-1", "subagents", "agent-a.json"), `{}`)
	if err := DeleteState("cs-1"); err != nil {
		t.Fatalf("DeleteState: %v", err)
	}
	for _, p := range []string{SessionFile("cs-1"), filepath.Join(SessionsDir(), "cs-1")} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still present", p)
		}
	}
	if err := DeleteState("../escape"); err == nil {
		t.Error("DeleteState accepted an unsafe id")
	}
}

func TestPruneOrphanLegacyDirs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeFile(t, SessionFile("cs-live"), `{}`)
	writeFile(t, filepath.Join(SessionsDir(), "cs-live", "subagents", "agent-a.json"), `{}`)
	writeFile(t, filepath.Join(SessionsDir(), "cs-orphan", "subagents", "agent-a.json"), `{}`)

	removed, err := PruneOrphanLegacyDirs()
	if err != nil || removed != 1 {
		t.Fatalf("PruneOrphanLegacyDirs = %d, %v; want 1, nil", removed, err)
	}
	if _, err := os.Stat(filepath.Join(SessionsDir(), "cs-orphan")); !errors.Is(err, os.ErrNotExist) {
		t.Error("orphan legacy dir still present")
	}
	if _, err := os.Stat(filepath.Join(SessionsDir(), "cs-live")); err != nil {
		t.Errorf("live session's legacy dir pruned before its migration: %v", err)
	}
}

func TestSaveState_ConcurrentWritersNeverCorruptTheState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	big := map[string]bool{}
	for i := 0; i < 600; i++ {
		big[time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC).Add(time.Duration(i)*time.Minute).Format("200601021504")] = true
	}
	done := make(chan struct{})
	for w := 0; w < 4; w++ {
		go func(w int) {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 50; i++ {
				s := &State{ClaudeSessionID: "cs-race", JSONLPath: "/p/cs-race.jsonl"}
				if w%2 == 0 {
					s.EmittedMinutes = big
				}
				_ = SaveState(s)
			}
		}(w)
	}
	for w := 0; w < 4; w++ {
		<-done
	}
	if _, err := LoadState("cs-race"); err != nil {
		t.Fatalf("state corrupted by concurrent writers: %v", err)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(SessionsDir(), "*.tmp")); len(leftovers) != 0 {
		t.Errorf("temp files left behind: %v", leftovers)
	}
}
