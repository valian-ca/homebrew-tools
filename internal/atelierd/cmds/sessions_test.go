package cmds

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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

func TestShouldSpawnWatcher(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	now := time.Now().UTC()
	jsonl := filepath.Join(t.TempDir(), "cs.jsonl")
	writeJSONL(t, jsonl, `{"type":"user","promptId":"p","message":{"role":"user","content":"x"}}`)
	size := int64(len(`{"type":"user","promptId":"p","message":{"role":"user","content":"x"}}`) + 1)

	cases := []struct {
		name  string
		state *transcript.State
		want  bool
	}{
		{"active session, jsonl absent", &transcript.State{JSONLPath: "/nonexistent.jsonl", LastActivityAt: now}, true},
		{"dormant, jsonl absent", &transcript.State{JSONLPath: "/nonexistent.jsonl", LastActivityAt: now.Add(-time.Hour)}, false},
		{"dormant, fully consumed", &transcript.State{JSONLPath: jsonl, Offset: size, LastActivityAt: now.Add(-time.Hour)}, false},
		{"dormant, unconsumed bytes", &transcript.State{JSONLPath: jsonl, Offset: 0, LastActivityAt: now.Add(-time.Hour)}, true},
		{"dormant, truncated below offset", &transcript.State{JSONLPath: jsonl, Offset: size + 100, LastActivityAt: now.Add(-time.Hour)}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldSpawnWatcher(tc.state, time.Now()); got != tc.want {
				t.Fatalf("shouldSpawnWatcher(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
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
		subagentDirManager(ctx, "cs-parent", parentJSONL, newActivityTracker())
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
		subagentDirManager(ctx, "cs-quiet", parentJSONL, newActivityTracker())
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

func countAssistantEnvelopes(t *testing.T) int {
	t.Helper()
	files, err := outbox.List()
	if err != nil {
		t.Fatalf("outbox.List: %v", err)
	}
	count := 0
	for _, f := range files {
		env, err := outbox.Read(f)
		if err != nil {
			t.Fatalf("outbox.Read: %v", err)
		}
		if env.Type == "hook:assistant-turn" {
			count++
		}
	}
	return count
}

// AC 1 (VAL-287): a startup over a large fleet of dormant states must watch
// only the active sessions. Goroutine growth is the observable proxy for
// watcher count — each spawned watcher pins several goroutines (watcher loop,
// subagent manager, fsnotify), so 1 000 wrongly spawned dormants would blow
// far past the threshold.
func TestSessionsManagerLoop_StartupSpawnsOnlyActiveWatchers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	old, oldPre := sessionPollInterval, subagentPreAttachInterval
	sessionPollInterval = 40 * time.Millisecond
	subagentPreAttachInterval = 40 * time.Millisecond
	t.Cleanup(func() { sessionPollInterval = old; subagentPreAttachInterval = oldPre })

	jsonlRoot := t.TempDir()
	stale := time.Now().UTC().Add(-2 * time.Hour)
	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("cs-dormant-%04d", i)
		if err := transcript.SaveState(&transcript.State{
			ClaudeSessionID: id,
			JSONLPath:       filepath.Join(jsonlRoot, id+".jsonl"),
			LastActivityAt:  stale,
		}); err != nil {
			t.Fatalf("save dormant %s: %v", id, err)
		}
	}

	activeJSONL := filepath.Join(jsonlRoot, "cs-active.jsonl")
	writeJSONL(t, activeJSONL, `{"type":"assistant","message":{"id":"msg_a","model":"claude-active-model","usage":{"input_tokens":1,"output_tokens":1},"content":[{"type":"text","text":"hi"}]}}`)
	if err := transcript.SaveState(&transcript.State{
		ClaudeSessionID: "cs-active",
		JSONLPath:       activeJSONL,
		LastActivityAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save active state: %v", err)
	}

	baseline := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		sessionsManagerLoop(ctx, nil)
	}()

	ok := waitFor(t, 5*time.Second, func() bool {
		_, has := collectEnvelopesByModel(t)["claude-active-model"]
		return has
	})
	if !ok {
		t.Fatal("active session was not consumed within deadline")
	}

	if !waitFor(t, 3*time.Second, func() bool { return runtime.NumGoroutine()-baseline <= 60 }) {
		t.Errorf("goroutine growth = %d, want <= 60 — dormant states are getting watchers", runtime.NumGoroutine()-baseline)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sessionsManagerLoop did not exit after ctx cancel")
	}
}

// AC 2 (VAL-287): a watcher whose session tree is idle past sessionIdleTimeout
// returns on its own — ctx is never cancelled here.
func TestRunSessionWatcher_IdleExit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	old, oldPre, oldIdle := sessionPollInterval, subagentPreAttachInterval, sessionIdleTimeout
	sessionPollInterval = 20 * time.Millisecond
	subagentPreAttachInterval = 20 * time.Millisecond
	sessionIdleTimeout = 80 * time.Millisecond
	t.Cleanup(func() {
		sessionPollInterval = old
		subagentPreAttachInterval = oldPre
		sessionIdleTimeout = oldIdle
	})

	jsonl := filepath.Join(t.TempDir(), "cs-idle.jsonl")
	line := `{"type":"user","promptId":"p1","message":{"role":"user","content":"x"}}`
	writeJSONL(t, jsonl, line)
	if err := transcript.SaveState(&transcript.State{
		ClaudeSessionID: "cs-idle",
		JSONLPath:       jsonl,
		Offset:          int64(len(line) + 1),
		LastActivityAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save state: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		runSessionWatcher(context.Background(), "cs-idle")
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runSessionWatcher did not idle-exit within deadline")
	}
}

func TestRunSubagentWatcher_IdleExit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	old, oldIdle := sessionPollInterval, sessionIdleTimeout
	sessionPollInterval = 20 * time.Millisecond
	sessionIdleTimeout = 80 * time.Millisecond
	t.Cleanup(func() { sessionPollInterval = old; sessionIdleTimeout = oldIdle })

	jsonl := filepath.Join(t.TempDir(), "cs-p", subagentDirName, "agent-i.jsonl")
	line := `{"type":"user","promptId":"p1","message":{"role":"user","content":"x"}}`
	writeJSONL(t, jsonl, line)
	key := transcript.SubagentWatcherKey("cs-p", "agent-i")
	if err := transcript.SaveState(&transcript.State{
		ClaudeSessionID: "cs-p",
		WatcherKey:      key,
		JSONLPath:       jsonl,
		Offset:          int64(len(line) + 1),
		LastActivityAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save state: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		runSubagentWatcher(context.Background(), key, newActivityTracker())
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runSubagentWatcher did not idle-exit within deadline")
	}
}

// AC 2 + AC 3 (VAL-287), full manager cycle: consume, idle-exit, stay dormant
// (no respawn), then revive from the persisted offset when new lines land —
// exactly one envelope per line, no re-emission, no loss.
func TestSessionsManagerLoop_IdleExitThenDormantRevival(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	old, oldPre, oldIdle, oldDormant := sessionPollInterval, subagentPreAttachInterval, sessionIdleTimeout, dormantScanInterval
	sessionPollInterval = 20 * time.Millisecond
	subagentPreAttachInterval = 20 * time.Millisecond
	sessionIdleTimeout = 80 * time.Millisecond
	dormantScanInterval = 50 * time.Millisecond
	t.Cleanup(func() {
		sessionPollInterval = old
		subagentPreAttachInterval = oldPre
		sessionIdleTimeout = oldIdle
		dormantScanInterval = oldDormant
	})

	jsonl := filepath.Join(t.TempDir(), "cs-cycle.jsonl")
	writeJSONL(t, jsonl, `{"type":"assistant","message":{"id":"msg_1","model":"claude-first-line","usage":{"input_tokens":1,"output_tokens":1},"content":[{"type":"text","text":"one"}]}}`)
	if err := transcript.SaveState(&transcript.State{
		ClaudeSessionID: "cs-cycle",
		JSONLPath:       jsonl,
		LastActivityAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save state: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		sessionsManagerLoop(ctx, nil)
	}()

	if !waitFor(t, 3*time.Second, func() bool {
		_, has := collectEnvelopesByModel(t)["claude-first-line"]
		return has
	}) {
		t.Fatal("first line was not consumed within deadline")
	}

	time.Sleep(300 * time.Millisecond)

	f, err := os.OpenFile(jsonl, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open jsonl for append: %v", err)
	}
	if _, err := f.WriteString(`{"type":"assistant","message":{"id":"msg_2","model":"claude-second-line","usage":{"input_tokens":1,"output_tokens":1},"content":[{"type":"text","text":"two"}]}}` + "\n"); err != nil {
		t.Fatalf("append second line: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close jsonl: %v", err)
	}

	if !waitFor(t, 5*time.Second, func() bool {
		_, has := collectEnvelopesByModel(t)["claude-second-line"]
		return has
	}) {
		t.Fatal("dormant session was not revived within deadline")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sessionsManagerLoop did not exit after ctx cancel")
	}

	if got := countAssistantEnvelopes(t); got != 2 {
		t.Errorf("assistant envelopes = %d, want exactly 2 (one per line, no re-emission)", got)
	}
}

// AC 5 (VAL-287): the GC step removes states whose JSONL is gone and leaves
// live states untouched, pruning emptied subagent directories. Two guards:
// an active state survives a transiently missing JSONL, and a subagent
// whose parent state is gone is purged even if its own JSONL still exists.
func TestRunStateGC_RemovesOrphansKeepsLive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	jsonlRoot := t.TempDir()
	liveJSONL := filepath.Join(jsonlRoot, "cs-live.jsonl")
	writeJSONL(t, liveJSONL, `{"type":"user","promptId":"p","message":{"role":"user","content":"x"}}`)
	headlessJSONL := filepath.Join(jsonlRoot, "cs-headless", subagentDirName, "agent-h.jsonl")
	writeJSONL(t, headlessJSONL, `{"type":"user","promptId":"p","message":{"role":"user","content":"x"}}`)

	states := []*transcript.State{
		{ClaudeSessionID: "cs-live", JSONLPath: liveJSONL},
		{ClaudeSessionID: "cs-orphan", JSONLPath: "/nonexistent/cs-orphan.jsonl"},
		{
			ClaudeSessionID: "cs-orphan",
			WatcherKey:      transcript.SubagentWatcherKey("cs-orphan", "agent-1"),
			JSONLPath:       "/nonexistent/cs-orphan/subagents/agent-1.jsonl",
		},
		{ClaudeSessionID: "cs-active-gone", JSONLPath: "/nonexistent/cs-active-gone.jsonl", LastActivityAt: time.Now().UTC()},
		{
			ClaudeSessionID: "cs-headless",
			WatcherKey:      transcript.SubagentWatcherKey("cs-headless", "agent-h"),
			JSONLPath:       headlessJSONL,
		},
		{
			ClaudeSessionID: "cs-live",
			WatcherKey:      transcript.SubagentWatcherKey("cs-live", "agent-gone"),
			JSONLPath:       "/nonexistent/cs-live/subagents/agent-gone.jsonl",
		},
	}
	for _, s := range states {
		if err := transcript.SaveState(s); err != nil {
			t.Fatalf("save %s: %v", s.Key(), err)
		}
	}

	runStateGC()

	if _, err := transcript.LoadState("cs-live"); err != nil {
		t.Errorf("live state was removed: %v", err)
	}
	if _, err := transcript.LoadState("cs-orphan"); !os.IsNotExist(err) {
		t.Errorf("orphan parent state should be gone, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(transcript.SessionsDir(), "cs-orphan")); !os.IsNotExist(err) {
		t.Errorf("orphan subagent dir tree should be pruned, got err=%v", err)
	}
	if _, err := transcript.LoadState("cs-active-gone"); err != nil {
		t.Errorf("active state must survive a transiently missing JSONL: %v", err)
	}
	if _, err := transcript.LoadState(transcript.SubagentWatcherKey("cs-headless", "agent-h")); !os.IsNotExist(err) {
		t.Errorf("subagent without a parent state should be gone, got err=%v", err)
	}
	if _, err := transcript.LoadState(transcript.SubagentWatcherKey("cs-live", "agent-gone")); !os.IsNotExist(err) {
		t.Errorf("subagent with a live parent but missing JSONL should be gone, got err=%v", err)
	}
	if _, err := transcript.LoadState("cs-live"); err != nil {
		t.Errorf("live parent must survive its subagent's purge: %v", err)
	}
}

// The dormant-skip branch of subagentDirManager: a subagent whose persisted
// state is dormant and fully consumed must not get a watcher when its file
// is rediscovered. Goroutine growth is the observable, as in the AC 1 test.
func TestSubagentDirManager_SkipsDormantFullyConsumedStates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	old, oldPre := sessionPollInterval, subagentPreAttachInterval
	sessionPollInterval = 30 * time.Millisecond
	subagentPreAttachInterval = 30 * time.Millisecond
	t.Cleanup(func() { sessionPollInterval = old; subagentPreAttachInterval = oldPre })

	jsonlRoot := t.TempDir()
	parentJSONL := filepath.Join(jsonlRoot, "cs-parent.jsonl")
	writeJSONL(t, parentJSONL, `{"type":"user","promptId":"p","message":{"role":"user","content":"x"}}`)

	subagentDir := filepath.Join(strings.TrimSuffix(parentJSONL, ".jsonl"), subagentDirName)
	stale := time.Now().UTC().Add(-2 * time.Hour)
	line := `{"type":"assistant","message":{"id":"msg_d","model":"claude-dormant-model","usage":{"input_tokens":1,"output_tokens":1},"content":[{"type":"text","text":"old"}]}}`
	for i := 0; i < 50; i++ {
		agentBase := fmt.Sprintf("agent-%02d", i)
		jsonl := filepath.Join(subagentDir, agentBase+".jsonl")
		writeJSONL(t, jsonl, line)
		if err := transcript.SaveState(&transcript.State{
			ClaudeSessionID: "cs-parent",
			WatcherKey:      transcript.SubagentWatcherKey("cs-parent", agentBase),
			JSONLPath:       jsonl,
			Offset:          int64(len(line) + 1),
			LastActivityAt:  stale,
		}); err != nil {
			t.Fatalf("save dormant subagent state %s: %v", agentBase, err)
		}
	}

	baseline := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		subagentDirManager(ctx, "cs-parent", parentJSONL, newActivityTracker())
	}()

	time.Sleep(200 * time.Millisecond)

	if !waitFor(t, 3*time.Second, func() bool { return runtime.NumGoroutine()-baseline <= 30 }) {
		t.Errorf("goroutine growth = %d, want <= 30 — dormant subagent states are getting watchers", runtime.NumGoroutine()-baseline)
	}
	if got := countAssistantEnvelopes(t); got != 0 {
		t.Errorf("dormant fully-consumed subagents produced %d envelopes, want 0", got)
	}

	revived := filepath.Join(subagentDir, "agent-07.jsonl")
	f, err := os.OpenFile(revived, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open %s for append: %v", revived, err)
	}
	if _, err := f.WriteString(`{"type":"assistant","message":{"id":"msg_r","model":"claude-revived-model","usage":{"input_tokens":1,"output_tokens":1},"content":[{"type":"text","text":"new"}]}}` + "\n"); err != nil {
		t.Fatalf("append revival line: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close revived jsonl: %v", err)
	}

	if !waitFor(t, 5*time.Second, func() bool {
		_, has := collectEnvelopesByModel(t)["claude-revived-model"]
		return has
	}) {
		t.Fatal("dormant subagent with new bytes was not revived within deadline")
	}
	if got := countAssistantEnvelopes(t); got != 1 {
		t.Errorf("revived subagent envelopes = %d, want exactly 1 (offset resume, no re-emission)", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("subagentDirManager did not exit after ctx cancel")
	}
}
