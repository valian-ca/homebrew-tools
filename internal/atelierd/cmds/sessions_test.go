package cmds

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/outbox"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/transcript"
)

func writeJSONL(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func waitFor(t *testing.T, deadline time.Duration, cond func() bool) bool {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

func collectEnvelopesByModel(t *testing.T) map[string]string {
	t.Helper()
	files, err := outbox.List()
	if err != nil {
		t.Fatalf("outbox.List: %v", err)
	}
	out := map[string]string{}
	for _, f := range files {
		env, err := outbox.Read(f)
		if err != nil {
			t.Fatalf("outbox.Read: %v", err)
		}
		if env.Type != "hook:assistant-turn" {
			continue
		}
		model, _ := env.Payload["model"].(string)
		out[model] = env.ClaudeSessionID
	}
	return out
}

func TestSessionsManagerLoop_DoesNotSpawnForOrphanSubagent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	old, oldPre := sessionPollInterval, subagentPreAttachInterval
	sessionPollInterval = 30 * time.Millisecond
	subagentPreAttachInterval = 30 * time.Millisecond
	t.Cleanup(func() { sessionPollInterval = old; subagentPreAttachInterval = oldPre })

	orphan := &transcript.State{
		ClaudeSessionID: "cs-orphan-parent",
		WatcherKey:      transcript.SubagentWatcherKey("cs-orphan-parent", "agent-orphan"),
		JSONLPath:       filepath.Join(t.TempDir(), "cs-orphan-parent", subagentDirName, "agent-orphan.jsonl"),
	}
	if err := transcript.SaveState(orphan); err != nil {
		t.Fatalf("save orphan: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		sessionsManagerLoop(ctx, nil)
	}()

	time.Sleep(150 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("sessionsManagerLoop did not exit after ctx cancel")
	}

	files, err := outbox.List()
	if err != nil {
		t.Fatalf("outbox.List: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("orphan subagent must not produce envelopes (no parent registered), got %d files in outbox", len(files))
	}
}

func TestShouldHandleSubagentEvent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ev   fsnotify.Event
		want bool
	}{
		{"create on agent file", fsnotify.Event{Name: "/p/subagents/agent-1.jsonl", Op: fsnotify.Create}, true},
		{"rename on agent file", fsnotify.Event{Name: "/p/subagents/agent-1.jsonl", Op: fsnotify.Rename}, true},
		{"write on agent file", fsnotify.Event{Name: "/p/subagents/agent-1.jsonl", Op: fsnotify.Write}, true},
		{"create on non-agent file", fsnotify.Event{Name: "/p/subagents/notes.jsonl", Op: fsnotify.Create}, false},
		{"create on non-jsonl agent", fsnotify.Event{Name: "/p/subagents/agent-1.txt", Op: fsnotify.Create}, false},
		{"chmod on agent", fsnotify.Event{Name: "/p/subagents/agent-1.jsonl", Op: fsnotify.Chmod}, false},
		{"remove on agent", fsnotify.Event{Name: "/p/subagents/agent-1.jsonl", Op: fsnotify.Remove}, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := shouldHandleSubagentEvent(tc.ev)
			if got != tc.want {
				t.Fatalf("shouldHandleSubagentEvent(%+v) = %v, want %v", tc.ev, got, tc.want)
			}
		})
	}
}

// A subagent's State carries the parent's ClaudeSessionID with a distinct
// WatcherKey; every envelope produced by consume() inherits that
// ClaudeSessionID while preserving the subagent's actual model. This is
// what unlocks aggregatePhaseRun on the dashboard backend folding tokens of
// distinct models into the same phaseRun.
func TestConsume_SubagentEnvelopesCarryParentClaudeSessionID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	jsonlRoot := t.TempDir()
	parentJSONL := filepath.Join(jsonlRoot, "cs-parent.jsonl")
	writeJSONL(t, parentJSONL, `{"type":"assistant","message":{"id":"msg_opus","model":"claude-opus-4-7","usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":4},"content":[{"type":"text","text":"parent"}]}}`)

	subagentDir := filepath.Join(strings.TrimSuffix(parentJSONL, ".jsonl"), subagentDirName)
	subagentJSONL := filepath.Join(subagentDir, "agent-X.jsonl")
	writeJSONL(t, subagentJSONL, `{"type":"assistant","message":{"id":"msg_haiku","model":"claude-haiku-4-5-20251001","usage":{"input_tokens":7,"output_tokens":2,"cache_read_input_tokens":1},"content":[{"type":"text","text":"sub"}]}}`)

	if err := transcript.SaveState(&transcript.State{
		ClaudeSessionID: "cs-parent",
		JSONLPath:       parentJSONL,
		LastActivityAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save parent state: %v", err)
	}

	subagentKey := filepath.Join("cs-parent", subagentDirName, "agent-X")
	if err := transcript.SaveState(&transcript.State{
		ClaudeSessionID: "cs-parent",
		WatcherKey:      subagentKey,
		JSONLPath:       subagentJSONL,
		LastActivityAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save subagent state: %v", err)
	}

	parent, _ := transcript.LoadState("cs-parent")
	sub, _ := transcript.LoadState(subagentKey)

	ctx := context.Background()
	consume(ctx, parent)
	consume(ctx, sub)

	byModel := collectEnvelopesByModel(t)
	if len(byModel) != 2 {
		t.Fatalf("want 2 distinct models in outbox, got %d (%v)", len(byModel), byModel)
	}
	for model, sessionID := range byModel {
		if sessionID != "cs-parent" {
			t.Errorf("model %q envelope ClaudeSessionID = %q, want cs-parent", model, sessionID)
		}
	}
	if _, ok := byModel["claude-opus-4-7"]; !ok {
		t.Errorf("missing parent Opus envelope")
	}
	if _, ok := byModel["claude-haiku-4-5-20251001"]; !ok {
		t.Errorf("missing subagent Haiku envelope")
	}
}

// Exercises the full dir-manager + per-file watcher path: parent registered,
// subagent dir created lazily by Claude Code, agent file appears, manager
// spawns its watcher, runSubagentWatcher consumes the file, envelopes carry
// the parent's claudeSessionID. Before the subagent dir exists the manager
// polls silently — the test waits a few ticks before creating the dir and
// never inspects the log file.
func TestSubagentDirManager_DiscoversAndAttributesToParent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	old, oldPre := sessionPollInterval, subagentPreAttachInterval
	sessionPollInterval = 30 * time.Millisecond
	subagentPreAttachInterval = 30 * time.Millisecond
	t.Cleanup(func() { sessionPollInterval = old; subagentPreAttachInterval = oldPre })

	jsonlRoot := t.TempDir()
	parentJSONL := filepath.Join(jsonlRoot, "cs-parent.jsonl")
	writeJSONL(t, parentJSONL, `{"type":"assistant","message":{"id":"msg_parent","model":"claude-opus-4-7","usage":{"input_tokens":1,"output_tokens":1},"content":[{"type":"text","text":"hi"}]}}`)

	if err := transcript.SaveState(&transcript.State{
		ClaudeSessionID: "cs-parent",
		JSONLPath:       parentJSONL,
		LastActivityAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save parent state: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		subagentDirManager(ctx, "cs-parent", parentJSONL)
	}()

	time.Sleep(100 * time.Millisecond)

	subagentDir := filepath.Join(strings.TrimSuffix(parentJSONL, ".jsonl"), subagentDirName)
	subagentJSONL := filepath.Join(subagentDir, "agent-Z.jsonl")
	writeJSONL(t, subagentJSONL, `{"type":"assistant","message":{"id":"msg_z","model":"claude-haiku-4-5-20251001","usage":{"input_tokens":3,"output_tokens":4,"cache_read_input_tokens":2},"content":[{"type":"text","text":"hi-from-haiku"}]}}`)

	ok := waitFor(t, 3*time.Second, func() bool {
		byModel := collectEnvelopesByModel(t)
		_, hasHaiku := byModel["claude-haiku-4-5-20251001"]
		return hasHaiku
	})
	if !ok {
		t.Fatalf("subagent envelopes did not appear in outbox within deadline; outbox=%v", collectEnvelopesByModel(t))
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("subagentDirManager did not exit within deadline after ctx cancel")
	}

	byModel := collectEnvelopesByModel(t)
	haikuSession, ok := byModel["claude-haiku-4-5-20251001"]
	if !ok {
		t.Fatalf("missing Haiku envelope: %v", byModel)
	}
	if haikuSession != "cs-parent" {
		t.Errorf("Haiku envelope claudeSessionId = %q, want cs-parent (parent's id)", haikuSession)
	}

	subKey := filepath.Join("cs-parent", subagentDirName, "agent-Z")
	got, err := transcript.LoadState(subKey)
	if err != nil {
		t.Fatalf("LoadState %s: %v", subKey, err)
	}
	if got.Offset == 0 {
		t.Errorf("subagent state Offset = 0, want > 0 (consumed bytes should have advanced offset)")
	}
	if got.ClaudeSessionID != "cs-parent" {
		t.Errorf("subagent state ClaudeSessionID = %q, want cs-parent", got.ClaudeSessionID)
	}
	if got.WatcherKey != subKey {
		t.Errorf("subagent state WatcherKey = %q, want %q", got.WatcherKey, subKey)
	}
}

// A parent session that never invokes a subagent must produce zero log noise
// and no state files under <parent>/subagents/.
func TestSubagentDirManager_SilentWhenSubagentDirAbsent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	old, oldPre := sessionPollInterval, subagentPreAttachInterval
	sessionPollInterval = 20 * time.Millisecond
	subagentPreAttachInterval = 20 * time.Millisecond
	t.Cleanup(func() { sessionPollInterval = old; subagentPreAttachInterval = oldPre })

	jsonlRoot := t.TempDir()
	parentJSONL := filepath.Join(jsonlRoot, "cs-quiet.jsonl")
	writeJSONL(t, parentJSONL, `{"type":"user","promptId":"p","message":{"role":"user","content":"x"}}`)

	if err := transcript.SaveState(&transcript.State{
		ClaudeSessionID: "cs-quiet",
		JSONLPath:       parentJSONL,
		LastActivityAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save parent state: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		subagentDirManager(ctx, "cs-quiet", parentJSONL)
	}()

	time.Sleep(150 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("subagentDirManager did not exit after ctx cancel")
	}

	subagentStateDir := filepath.Join(transcript.SessionsDir(), "cs-quiet", subagentDirName)
	if _, err := os.Stat(subagentStateDir); err == nil {
		t.Errorf("subagent state dir %s should not exist for a parent without subagents", subagentStateDir)
	}
}
