package cmds

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/outbox"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/transcript"
)

// fixtureSessionID and fixtureMinutes describe transcript/testdata/forge-session:
// a real forge session tree (parent, 13 subagents, 4 Workflow agents) stripped
// to type/timestamp/sessionId. fixtureMinutes is the number of distinct UTC
// minutes over all its timestamped agent and parent lines, computed once with
// jq at fixture creation; the parent alone covers 51 of them.
const (
	fixtureSessionID = "fixture-forge-session"
	fixtureMinutes   = 78
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

func appendRaw(t *testing.T, path, raw string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(raw); err != nil {
		t.Fatalf("append %s: %v", path, err)
	}
}

func appendJSONL(t *testing.T, path string, lines ...string) {
	t.Helper()
	appendRaw(t, path, strings.Join(lines, "\n")+"\n")
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

func outboxEnvelopes(t *testing.T) []*outbox.Envelope {
	t.Helper()
	files, err := outbox.List()
	if err != nil {
		t.Fatalf("outbox.List: %v", err)
	}
	envs := make([]*outbox.Envelope, 0, len(files))
	for _, f := range files {
		env, err := outbox.Read(f)
		if err != nil {
			t.Fatalf("outbox.Read: %v", err)
		}
		envs = append(envs, env)
	}
	return envs
}

func countByType(t *testing.T) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for _, env := range outboxEnvelopes(t) {
		counts[env.Type]++
	}
	return counts
}

func hasEnvelope(t *testing.T, id string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".atelier", "outbox", id+".json"))
	return err == nil
}

// drainOutbox stands for a successful ship: the queue empties.
func drainOutbox(t *testing.T) {
	t.Helper()
	files, _ := outbox.List()
	for _, f := range files {
		_ = outbox.Delete(f)
	}
}

func copyFixture(t *testing.T) string {
	t.Helper()
	src := filepath.Join("..", "transcript", "testdata", "forge-session")
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, raw, 0o600)
	})
	if err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	return filepath.Join(dst, fixtureSessionID+".jsonl")
}

func mustRegister(t *testing.T, id, jsonl string) {
	t.Helper()
	if err := registerSession(id, jsonl); err != nil {
		t.Fatalf("registerSession: %v", err)
	}
}

func mustRead(t *testing.T, id string) *transcript.State {
	t.Helper()
	s, err := readSessionTree(context.Background(), id)
	if err != nil {
		t.Fatalf("readSessionTree: %v", err)
	}
	return s
}

func lineAt(typ string, at time.Time) string {
	return fmt.Sprintf(`{"type":%q,"timestamp":%q}`, typ, at.UTC().Format("2006-01-02T15:04:05.000Z"))
}

// testMinute anchors every relative minute of a test run, so a check never
// recomputes a minute that rolled over since its line was written.
var testMinute = time.Now().UTC().Truncate(time.Minute)

func recentMinute(ago int) time.Time {
	return testMinute.Add(-time.Duration(ago) * time.Minute)
}

func TestReadSessionTree_FixtureReplayEmitsOneHeartbeatPerMinute(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	parent := copyFixture(t)
	mustRegister(t, fixtureSessionID, parent)

	mustRead(t, fixtureSessionID)

	counts := countByType(t)
	if counts["activity:minute"] != fixtureMinutes {
		t.Errorf("activity:minute = %d, want %d (distinct minutes of the fixture)", counts["activity:minute"], fixtureMinutes)
	}
	if counts["transcript:custom-title"] != 1 {
		t.Errorf("transcript:custom-title = %d, want 1 (unchanged title deduped)", counts["transcript:custom-title"])
	}
	for typ := range counts {
		if typ != "activity:minute" && typ != "transcript:custom-title" {
			t.Errorf("reader queued a %q event", typ)
		}
	}
	for _, env := range outboxEnvelopes(t) {
		if env.ClaudeSessionID != fixtureSessionID {
			t.Errorf("envelope %s carries %q, want the parent session id", env.ULID, env.ClaudeSessionID)
		}
		if env.Type == "activity:minute" && (env.TS == nil || env.ULID != transcript.ActivityMinuteID(fixtureSessionID, *env.TS)) {
			t.Errorf("heartbeat %s is not keyed by its minute (TS %v)", env.ULID, env.TS)
		}
	}

	drainOutbox(t)
	mustRead(t, fixtureSessionID)
	if n := len(outboxEnvelopes(t)); n != 0 {
		t.Errorf("second read queued %d envelopes, want 0", n)
	}
}

func TestReadSessionTree_ResumeAndCompactReEmitNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	parent := copyFixture(t)
	mustRegister(t, fixtureSessionID, parent)
	mustRead(t, fixtureSessionID)
	drainOutbox(t)

	metadata := []string{
		`{"type":"mode","mode":"default","sessionId":"fixture-forge-session"}`,
		`{"type":"permission-mode","permissionMode":"auto","sessionId":"fixture-forge-session"}`,
		`{"type":"atis-latch","sessionId":"fixture-forge-session"}`,
		`{"type":"last-prompt","sessionId":"fixture-forge-session"}`,
		`{"type":"custom-title","customTitle":"Fixture forge session","sessionId":"fixture-forge-session"}`,
		`{"type":"agent-name","sessionId":"fixture-forge-session"}`,
	}
	steps := []struct {
		name  string
		lines []string
		want  string
	}{
		{"resume", append(append([]string{}, metadata...), lineAt("user", recentMinute(3).Add(10*time.Second)), lineAt("assistant", recentMinute(3).Add(40*time.Second))), transcript.ActivityMinuteID(fixtureSessionID, recentMinute(3))},
		{"compact 1", append(append([]string{}, metadata...), lineAt("system", recentMinute(2).Add(5*time.Second)), lineAt("user", recentMinute(3).Add(50*time.Second))), transcript.ActivityMinuteID(fixtureSessionID, recentMinute(2))},
		{"compact 2", append(append([]string{}, metadata...), lineAt("system", recentMinute(1).Add(5*time.Second))), transcript.ActivityMinuteID(fixtureSessionID, recentMinute(1))},
	}
	for _, step := range steps {
		if err := runEmit(t, "hook:session-start", fixtureSessionID, "--data", "jsonlPath="+parent, "--data", "cwd=/repo"); err != nil {
			t.Fatalf("%s: emit session-start: %v", step.name, err)
		}
		appendJSONL(t, parent, step.lines...)
		mustRead(t, fixtureSessionID)

		envs := outboxEnvelopes(t)
		got := map[string]int{}
		for _, env := range envs {
			got[env.Type]++
			if env.Type == "activity:minute" && env.ULID != step.want {
				t.Errorf("%s: unexpected heartbeat %s, want only %s", step.name, env.ULID, step.want)
			}
		}
		if len(envs) != 2 || got["hook:session-start"] != 1 || got["activity:minute"] != 1 {
			t.Errorf("%s: outbox = %v, want one hook:session-start and one activity:minute", step.name, got)
		}
		drainOutbox(t)
	}
}

// An upgrade over a 0.18.1 tree (offsets at EOF) re-reads nothing.
func TestReadSessionTree_MigratedLegacyTreeReadsNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	parent := filepath.Join(dir, "cs-old.jsonl")
	agent := filepath.Join(dir, "cs-old", "subagents", "agent-a.jsonl")
	writeJSONL(t, parent, lineAt("user", recentMinute(10)), lineAt("assistant", recentMinute(9)))
	writeJSONL(t, agent, lineAt("user", recentMinute(8)))
	size := func(p string) int64 {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		return st.Size()
	}
	writeJSONL(t, transcript.SessionFile("cs-old"), fmt.Sprintf(
		`{"claudeSessionId":"cs-old","jsonlPath":%q,"offset":%d,"lastMsgId":"m","lastActivityAt":%q}`,
		parent, size(parent), time.Now().UTC().Format(time.RFC3339)))
	writeJSONL(t, filepath.Join(transcript.SessionsDir(), "cs-old", "subagents", "agent-a.json"), fmt.Sprintf(
		`{"claudeSessionId":"cs-old","watcherKey":"cs-old/subagents/agent-a","jsonlPath":%q,"offset":%d}`,
		agent, size(agent)))

	mustRead(t, "cs-old")

	if n := len(outboxEnvelopes(t)); n != 0 {
		t.Fatalf("first read after migration queued %d envelopes, want 0", n)
	}
	appendJSONL(t, agent, lineAt("assistant", recentMinute(2)))
	mustRead(t, "cs-old")
	if !hasEnvelope(t, transcript.ActivityMinuteID("cs-old", recentMinute(2))) || len(outboxEnvelopes(t)) != 1 {
		t.Fatalf("new subagent line after migration: outbox = %v, want its one heartbeat", countByType(t))
	}
}

func TestReadSessionTree_OutOfOrderMinutesAcrossFiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	parent := filepath.Join(t.TempDir(), "cs-1.jsonl")
	writeJSONL(t, parent, lineAt("assistant", recentMinute(1)))
	mustRegister(t, "cs-1", parent)
	mustRead(t, "cs-1")

	appendJSONL(t, filepath.Join(strings.TrimSuffix(parent, ".jsonl"), "subagents", "workflows", "wf_1", "agent-x.jsonl"),
		lineAt("user", recentMinute(6)), lineAt("assistant", recentMinute(1)))
	mustRead(t, "cs-1")

	for _, m := range []int{1, 6} {
		if !hasEnvelope(t, transcript.ActivityMinuteID("cs-1", recentMinute(m))) {
			t.Errorf("missing heartbeat for minute -%d", m)
		}
	}
	if n := len(outboxEnvelopes(t)); n != 2 {
		t.Errorf("outbox holds %d envelopes, want 2", n)
	}
}

func TestReadSessionTree_PartialLastLineWaitsForItsNewline(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	parent := filepath.Join(t.TempDir(), "cs-1.jsonl")
	full := lineAt("user", recentMinute(3))
	partial := lineAt("assistant", recentMinute(2))
	appendRaw(t, parent, full+"\n"+partial[:20])
	mustRegister(t, "cs-1", parent)

	s := mustRead(t, "cs-1")
	if got := s.Offset(parent); got != int64(len(full)+1) {
		t.Fatalf("offset = %d, want %d (end of the complete line)", got, len(full)+1)
	}
	if hasEnvelope(t, transcript.ActivityMinuteID("cs-1", recentMinute(2))) {
		t.Fatal("partial line derived before its newline")
	}

	appendRaw(t, parent, partial[20:]+"\n")
	mustRead(t, "cs-1")
	if !hasEnvelope(t, transcript.ActivityMinuteID("cs-1", recentMinute(2))) {
		t.Fatal("completed line not derived")
	}
}

func TestReadSessionTree_TruncatedFileRestartsWithoutRequeuingMinutes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	parent := filepath.Join(t.TempDir(), "cs-1.jsonl")
	agent := filepath.Join(strings.TrimSuffix(parent, ".jsonl"), "subagents", "agent-a.jsonl")
	writeJSONL(t, parent, lineAt("user", recentMinute(5)))
	writeJSONL(t, agent, lineAt("user", recentMinute(4)), lineAt("assistant", recentMinute(3)))
	mustRegister(t, "cs-1", parent)
	mustRead(t, "cs-1")
	drainOutbox(t)

	writeJSONL(t, agent, lineAt("user", recentMinute(4)))
	s := mustRead(t, "cs-1")

	if n := len(outboxEnvelopes(t)); n != 0 {
		t.Errorf("re-read after truncation queued %d envelopes, want 0 (minutes already emitted)", n)
	}
	if got, want := s.Offset(agent), int64(len(lineAt("user", recentMinute(4)))+1); got != want {
		t.Errorf("agent offset = %d, want %d", got, want)
	}
	if s.Offset(parent) == 0 {
		t.Error("truncating the agent transcript reset the parent offset")
	}
}

// A kill between the outbox write and the state save re-reads the lines; the
// re-derived heartbeats overwrite their own queued files.
func TestReadSessionTree_CrashBeforeStateSaveCollapsesLocally(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	parent := filepath.Join(t.TempDir(), "cs-1.jsonl")
	writeJSONL(t, parent, lineAt("user", recentMinute(3)), lineAt("assistant", recentMinute(2)))
	mustRegister(t, "cs-1", parent)
	before, err := os.ReadFile(transcript.SessionFile("cs-1"))
	if err != nil {
		t.Fatalf("read state: %v", err)
	}

	mustRead(t, "cs-1")
	if err := os.WriteFile(transcript.SessionFile("cs-1"), before, 0o600); err != nil {
		t.Fatalf("restore pre-read state: %v", err)
	}
	mustRead(t, "cs-1")

	if n := countByType(t)["activity:minute"]; n != 2 {
		t.Errorf("activity:minute files = %d, want 2 after the replay", n)
	}
}

func TestReadSessionTree_DropsHeartbeatsAheadOfTheClock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	parent := filepath.Join(t.TempDir(), "cs-1.jsonl")
	ahead := recentMinute(-10)
	writeJSONL(t, parent, lineAt("user", ahead), lineAt("assistant", recentMinute(1)))
	mustRegister(t, "cs-1", parent)

	s := mustRead(t, "cs-1")

	if hasEnvelope(t, transcript.ActivityMinuteID("cs-1", ahead)) {
		t.Error("heartbeat 10 min ahead of the clock was queued")
	}
	if !hasEnvelope(t, transcript.ActivityMinuteID("cs-1", recentMinute(1))) {
		t.Error("in-range heartbeat missing")
	}
	if s.EmittedMinutes[ahead.Format("200601021504")] {
		t.Error("dropped minute recorded as emitted")
	}
}

func TestHasUnconsumedBytes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now().UTC()
	dir := t.TempDir()
	parent := filepath.Join(dir, "cs.jsonl")
	line := lineAt("user", recentMinute(1))
	writeJSONL(t, parent, line)
	consumed := map[string]int64{"cs.jsonl": int64(len(line) + 1)}
	agentDir := filepath.Join(t.TempDir(), "cs-agent")
	agentParent := filepath.Join(agentDir, "cs.jsonl")
	writeJSONL(t, agentParent, line)
	writeJSONL(t, filepath.Join(agentDir, "cs", "subagents", "workflows", "wf_1", "agent-a.jsonl"), line)

	cases := []struct {
		name  string
		state *transcript.State
		want  bool
	}{
		{"transcript absent", &transcript.State{JSONLPath: "/nonexistent.jsonl", LastActivityAt: now}, false},
		{"dormant, fully consumed", &transcript.State{JSONLPath: parent, Offsets: consumed, LastActivityAt: now.Add(-time.Hour)}, false},
		{"dormant, unconsumed parent", &transcript.State{JSONLPath: parent, LastActivityAt: now.Add(-time.Hour)}, true},
		{"dormant, truncated below offset", &transcript.State{JSONLPath: parent, Offsets: map[string]int64{"cs.jsonl": 9999}, LastActivityAt: now.Add(-time.Hour)}, true},
		{"dormant, unconsumed Workflow agent", &transcript.State{JSONLPath: agentParent, Offsets: consumed, LastActivityAt: now.Add(-time.Hour)}, true},
	}
	for _, tc := range cases {
		if got := hasUnconsumedBytes(tc.state); got != tc.want {
			t.Errorf("hasUnconsumedBytes(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRunSessionReader_IdleExit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	oldPoll, oldIdle := treePollInterval, sessionIdleTimeout
	treePollInterval = 20 * time.Millisecond
	sessionIdleTimeout = 80 * time.Millisecond
	t.Cleanup(func() { treePollInterval = oldPoll; sessionIdleTimeout = oldIdle })

	parent := filepath.Join(t.TempDir(), "cs-idle.jsonl")
	writeJSONL(t, parent, lineAt("user", recentMinute(1)))
	mustRegister(t, "cs-idle", parent)

	done := make(chan struct{})
	go func() {
		defer close(done)
		runSessionReader(context.Background(), "cs-idle")
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runSessionReader did not idle-exit")
	}
}

func TestRunSessionReader_StaysAliveWhileAWorkflowAgentStreams(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	oldPoll, oldIdle := treePollInterval, sessionIdleTimeout
	treePollInterval = 20 * time.Millisecond
	sessionIdleTimeout = 300 * time.Millisecond
	t.Cleanup(func() { treePollInterval = oldPoll; sessionIdleTimeout = oldIdle })

	parent := filepath.Join(t.TempDir(), "cs-wf.jsonl")
	writeJSONL(t, parent, lineAt("user", recentMinute(2)))
	mustRegister(t, "cs-wf", parent)
	agent := filepath.Join(strings.TrimSuffix(parent, ".jsonl"), "subagents", "workflows", "wf_1", "agent-a.jsonl")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		runSessionReader(ctx, "cs-wf")
	}()

	for i := 0; i < 8; i++ {
		time.Sleep(100 * time.Millisecond)
		appendJSONL(t, agent, lineAt("assistant", recentMinute(1)))
	}
	select {
	case <-done:
		t.Fatal("reader idle-exited while its Workflow agent kept writing")
	default:
	}
	cancel()
	<-done
}

func TestSessionsManagerLoop_StartupSpawnsOnlyActiveReaders(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	old := sessionPollInterval
	sessionPollInterval = 40 * time.Millisecond
	t.Cleanup(func() { sessionPollInterval = old })

	root := t.TempDir()
	stale := time.Now().UTC().Add(-2 * time.Hour)
	for i := 0; i < 500; i++ {
		id := fmt.Sprintf("cs-dormant-%04d", i)
		if err := transcript.SaveState(&transcript.State{ClaudeSessionID: id, JSONLPath: filepath.Join(root, id+".jsonl"), LastActivityAt: stale}); err != nil {
			t.Fatalf("save dormant %s: %v", id, err)
		}
	}
	active := filepath.Join(root, "cs-active.jsonl")
	writeJSONL(t, active, lineAt("user", recentMinute(1)))
	mustRegister(t, "cs-active", active)

	baseline := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		sessionsManagerLoop(ctx, nil)
	}()

	if !waitFor(t, 5*time.Second, func() bool { return hasEnvelope(t, transcript.ActivityMinuteID("cs-active", recentMinute(1))) }) {
		t.Fatal("active session was not read")
	}
	if growth := runtime.NumGoroutine() - baseline; growth > 20 {
		t.Errorf("goroutine growth = %d, want <= 20 — dormant states are getting readers", growth)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sessionsManagerLoop did not exit after ctx cancel")
	}
}

// Full manager cycle: read, idle-exit, stay dormant, then revive when a
// Workflow agent — not the parent — writes again.
func TestSessionsManagerLoop_IdleExitThenRevivalFromAWorkflowAgent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	oldDiscovery, oldPoll, oldIdle, oldDormant := sessionPollInterval, treePollInterval, sessionIdleTimeout, dormantScanInterval
	sessionPollInterval = 20 * time.Millisecond
	treePollInterval = 20 * time.Millisecond
	sessionIdleTimeout = 80 * time.Millisecond
	dormantScanInterval = 50 * time.Millisecond
	t.Cleanup(func() {
		sessionPollInterval, treePollInterval, sessionIdleTimeout, dormantScanInterval = oldDiscovery, oldPoll, oldIdle, oldDormant
	})

	parent := filepath.Join(t.TempDir(), "cs-cycle.jsonl")
	writeJSONL(t, parent, lineAt("user", recentMinute(3)))
	mustRegister(t, "cs-cycle", parent)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		sessionsManagerLoop(ctx, nil)
	}()

	if !waitFor(t, 3*time.Second, func() bool { return hasEnvelope(t, transcript.ActivityMinuteID("cs-cycle", recentMinute(3))) }) {
		t.Fatal("first line was not read")
	}
	time.Sleep(300 * time.Millisecond)

	appendJSONL(t, filepath.Join(strings.TrimSuffix(parent, ".jsonl"), "subagents", "workflows", "wf_1", "agent-a.jsonl"),
		lineAt("assistant", recentMinute(1)))
	if !waitFor(t, 5*time.Second, func() bool { return hasEnvelope(t, transcript.ActivityMinuteID("cs-cycle", recentMinute(1))) }) {
		t.Fatal("dormant session was not revived by its Workflow agent")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sessionsManagerLoop did not exit after ctx cancel")
	}
	if n := countByType(t)["activity:minute"]; n != 2 {
		t.Errorf("activity:minute = %d, want 2 (no re-emission)", n)
	}
}

func TestRunStateGC_RemovesOrphansKeepsLive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	live := filepath.Join(root, "cs-live.jsonl")
	writeJSONL(t, live, lineAt("user", recentMinute(1)))

	for _, s := range []*transcript.State{
		{ClaudeSessionID: "cs-live", JSONLPath: live},
		{ClaudeSessionID: "cs-orphan", JSONLPath: "/nonexistent/cs-orphan.jsonl"},
		{ClaudeSessionID: "cs-active-gone", JSONLPath: "/nonexistent/cs-active-gone.jsonl", LastActivityAt: time.Now().UTC()},
	} {
		if err := transcript.SaveState(s); err != nil {
			t.Fatalf("save %s: %v", s.ClaudeSessionID, err)
		}
	}
	writeJSONL(t, filepath.Join(transcript.SessionsDir(), "cs-headless", "subagents", "agent-h.json"), `{}`)

	runStateGC()

	if _, err := transcript.LoadState("cs-live"); err != nil {
		t.Errorf("live state was removed: %v", err)
	}
	if _, err := transcript.LoadState("cs-orphan"); !os.IsNotExist(err) {
		t.Errorf("orphan state should be gone, got err=%v", err)
	}
	if _, err := transcript.LoadState("cs-active-gone"); err != nil {
		t.Errorf("active state must survive a transiently missing transcript: %v", err)
	}
	if _, err := os.Stat(filepath.Join(transcript.SessionsDir(), "cs-headless")); !os.IsNotExist(err) {
		t.Errorf("orphan 0.18.x subagent state dir should be pruned, got err=%v", err)
	}
}

// A session-start that moves the session to another transcript while a read
// is in flight must not be undone by the reader's save.
func TestReadSessionTree_DoesNotUndoAConcurrentReRegistration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "cs-1.jsonl")
	newPath := filepath.Join(dir, "moved", "cs-1.jsonl")
	writeJSONL(t, oldPath, lineAt("user", recentMinute(4)))
	writeJSONL(t, newPath, lineAt("user", recentMinute(2)))
	mustRegister(t, "cs-1", oldPath)

	reRegistered := false
	testHookBeforeSave = func() {
		if !reRegistered {
			reRegistered = true
			mustRegister(t, "cs-1", newPath)
		}
	}
	t.Cleanup(func() { testHookBeforeSave = nil })
	mustRead(t, "cs-1")
	testHookBeforeSave = nil

	s, err := transcript.LoadState("cs-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if s.JSONLPath != newPath {
		t.Fatalf("JSONLPath = %q, want the re-registered %q", s.JSONLPath, newPath)
	}
	mustRead(t, "cs-1")
	if !hasEnvelope(t, transcript.ActivityMinuteID("cs-1", recentMinute(2))) {
		t.Error("the new transcript was not read after the re-registration")
	}
}

func TestReadSessionTree_DoesNotResurrectADeletedState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	parent := filepath.Join(t.TempDir(), "cs-1.jsonl")
	writeJSONL(t, parent, lineAt("user", recentMinute(1)))
	mustRegister(t, "cs-1", parent)
	testHookBeforeSave = func() { _ = transcript.DeleteState("cs-1") }
	t.Cleanup(func() { testHookBeforeSave = nil })

	mustRead(t, "cs-1")

	if _, err := transcript.LoadState("cs-1"); !os.IsNotExist(err) {
		t.Fatalf("state deleted mid-read was saved back: err=%v", err)
	}
}

func TestRunSessionReader_StopsWhenItsStateDisappears(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	oldPoll, oldIdle := treePollInterval, sessionIdleTimeout
	treePollInterval = 20 * time.Millisecond
	sessionIdleTimeout = time.Hour
	t.Cleanup(func() { treePollInterval = oldPoll; sessionIdleTimeout = oldIdle })
	parent := filepath.Join(t.TempDir(), "cs-gone.jsonl")
	writeJSONL(t, parent, lineAt("user", recentMinute(1)))
	mustRegister(t, "cs-gone", parent)

	done := make(chan struct{})
	go func() {
		defer close(done)
		runSessionReader(context.Background(), "cs-gone")
	}()
	time.Sleep(60 * time.Millisecond)
	if err := transcript.DeleteState("cs-gone"); err != nil {
		t.Fatalf("DeleteState: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reader kept running after its state was deleted")
	}
}

func TestReadSessionTree_OutboxWriteFailureKeepsLinesForReRead(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	parent := filepath.Join(t.TempDir(), "cs-1.jsonl")
	writeJSONL(t, parent, lineAt("user", recentMinute(3)), lineAt("assistant", recentMinute(2)))
	mustRegister(t, "cs-1", parent)
	dir := filepath.Join(home, ".atelier", "outbox")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	mustRead(t, "cs-1")

	s, err := transcript.LoadState("cs-1")
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if s.Offset(parent) != 0 || len(s.EmittedMinutes) != 0 {
		t.Fatalf("failed write saved progress: offset %d, minutes %v", s.Offset(parent), s.EmittedMinutes)
	}

	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	mustRead(t, "cs-1")
	for _, m := range []int{2, 3} {
		if !hasEnvelope(t, transcript.ActivityMinuteID("cs-1", recentMinute(m))) {
			t.Errorf("minute -%d not queued after the outbox recovered", m)
		}
	}
}

func TestReadSessionTree_TranscriptNotYetCreated(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	parent := filepath.Join(t.TempDir(), "cs-1.jsonl")
	mustRegister(t, "cs-1", parent)

	mustRead(t, "cs-1")
	if n := len(outboxEnvelopes(t)); n != 0 {
		t.Fatalf("outbox holds %d envelopes before the transcript exists", n)
	}

	writeJSONL(t, parent, lineAt("user", recentMinute(1)))
	mustRead(t, "cs-1")
	if !hasEnvelope(t, transcript.ActivityMinuteID("cs-1", recentMinute(1))) {
		t.Fatal("transcript created after registration was not read")
	}
}

func TestSessionsManagerLoop_DoesNotReviveAStuckTreeEveryScan(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	oldDiscovery, oldPoll, oldIdle, oldDormant := sessionPollInterval, treePollInterval, sessionIdleTimeout, dormantScanInterval
	sessionPollInterval = 20 * time.Millisecond
	treePollInterval = 20 * time.Millisecond
	sessionIdleTimeout = 80 * time.Millisecond
	dormantScanInterval = 30 * time.Millisecond
	t.Cleanup(func() {
		sessionPollInterval, treePollInterval, sessionIdleTimeout, dormantScanInterval = oldDiscovery, oldPoll, oldIdle, oldDormant
	})

	parent := filepath.Join(t.TempDir(), "cs-stuck.jsonl")
	full := lineAt("user", recentMinute(3))
	cut := lineAt("assistant", recentMinute(2))
	appendRaw(t, parent, full+"\n"+cut[:20])
	if err := transcript.SaveState(&transcript.State{
		ClaudeSessionID: "cs-stuck",
		JSONLPath:       parent,
		Offsets:         map[string]int64{"cs-stuck.jsonl": int64(len(full) + 1)},
		LastActivityAt:  time.Now().UTC().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	var mu sync.Mutex
	starts := 0
	testHookReaderStarted = func(string) { mu.Lock(); starts++; mu.Unlock() }
	t.Cleanup(func() { testHookReaderStarted = nil })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		sessionsManagerLoop(ctx, nil)
	}()
	time.Sleep(400 * time.Millisecond)
	mu.Lock()
	stuckStarts := starts
	mu.Unlock()
	if stuckStarts != 1 {
		t.Errorf("stuck tree got %d readers over ~13 dormant scans, want 1 (once per size)", stuckStarts)
	}

	appendRaw(t, parent, cut[20:]+"\n")
	if !waitFor(t, 3*time.Second, func() bool { return hasEnvelope(t, transcript.ActivityMinuteID("cs-stuck", recentMinute(2))) }) {
		t.Fatal("tree not revived once its last line completed")
	}
	cancel()
	<-done
}
