package cmds

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/app"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/credentials"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/outbox"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/status"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/transcript"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/ulid"
)

// fakeFirestore mimics the /events :commit semantics verified on the emulator:
// a commit is atomic, a create-only write on an existing doc fails the whole
// commit with 409, and a doc the rules forbid fails it with 403.
type fakeFirestore struct {
	mu         sync.Mutex
	docs       map[string]time.Time
	forbidden  map[string]bool
	fail5xx    int
	failAfter  int
	commits    int
	sawNoGuard bool
}

func newFakeFirestore(t *testing.T) *fakeFirestore {
	t.Helper()
	f := &fakeFirestore{docs: map[string]time.Time{}, forbidden: map[string]bool{}, failAfter: -1}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	t.Cleanup(app.SetCommitURLForTest(srv.URL))
	return f
}

func (f *fakeFirestore) handle(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Writes []struct {
			Update struct {
				Name string `json:"name"`
			} `json:"update"`
			CurrentDocument *struct {
				Exists bool `json:"exists"`
			} `json:"currentDocument"`
		} `json:"writes"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	f.mu.Lock()
	defer f.mu.Unlock()
	f.commits++
	if f.failAfter >= 0 && f.commits > f.failAfter && f.fail5xx > 0 {
		f.fail5xx--
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	ids := make([]string, 0, len(body.Writes))
	for _, wr := range body.Writes {
		id := path.Base(wr.Update.Name)
		if wr.CurrentDocument == nil || wr.CurrentDocument.Exists {
			f.sawNoGuard = true
		}
		if _, exists := f.docs[id]; exists {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"status":"ALREADY_EXISTS"}}`))
			return
		}
		if f.forbidden[id] {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"status":"PERMISSION_DENIED"}}`))
			return
		}
		ids = append(ids, id)
	}
	now := time.Now()
	for _, id := range ids {
		f.docs[id] = now
	}
	_, _ = w.Write([]byte(`{}`))
}

func (f *fakeFirestore) has(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.docs[id]
	return ok
}

func (f *fakeFirestore) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.docs)
}

func newTestRunState() *runState {
	return &runState{
		creds: &credentials.Credentials{
			UID:              "uid-1",
			IDToken:          "tok",
			RefreshToken:     "refresh",
			IDTokenExpiresAt: time.Now().Add(time.Hour),
		},
		host:      "host-1",
		authState: status.AuthOk,
	}
}

func queueHeartbeat(t *testing.T, session string, minute time.Time) string {
	t.Helper()
	id := transcript.ActivityMinuteID(session, minute)
	if err := outbox.Write(&outbox.Envelope{
		ULID:            id,
		Type:            "activity:minute",
		ClaudeSessionID: session,
		Payload:         map[string]any{},
		CreatedAt:       time.Now().UTC(),
		TS:              &minute,
	}); err != nil {
		t.Fatalf("queue heartbeat: %v", err)
	}
	return id
}

func queueEvent(t *testing.T, session, typ string) string {
	t.Helper()
	id := ulid.New()
	if err := outbox.Write(&outbox.Envelope{ULID: id, Type: typ, ClaudeSessionID: session, Payload: map[string]any{}, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("queue event: %v", err)
	}
	return id
}

func outboxFiles(t *testing.T, suffix string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(os.Getenv("HOME"), ".atelier", "outbox"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read outbox: %v", err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), suffix) {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestTryShip_AlreadyExistingDocInsideABatchShipsTheRest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fs := newFakeFirestore(t)
	base := time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC)
	dup := queueHeartbeat(t, "cs-1", base)
	others := []string{
		queueHeartbeat(t, "cs-1", base.Add(time.Minute)),
		queueHeartbeat(t, "cs-1", base.Add(2*time.Minute)),
		queueEvent(t, "cs-1", "skill:phase-start"),
	}
	fs.docs[dup] = time.Now()

	tryShip(context.Background(), newTestRunState())

	for _, id := range others {
		if !fs.has(id) {
			t.Errorf("%s was not shipped behind the duplicate", id)
		}
	}
	if left := outboxFiles(t, ".json"); len(left) != 0 {
		t.Errorf("outbox still holds %v, want empty (409 counts as shipped)", left)
	}
	if rejected := outboxFiles(t, ".rejected"); len(rejected) != 0 {
		t.Errorf("409 quarantined %v", rejected)
	}
	if fs.sawNoGuard {
		t.Error("a write was sent without the exists=false precondition")
	}
}

// A 2 h catch-up crossing three batches, with an already-shipped minute in
// the second batch and a transient 5xx in the middle of its isolation: the
// first pass backs off, the next one finishes, nothing is quarantined.
func TestTryShip_BurstWithDuplicateThenTransientError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fs := newFakeFirestore(t)
	base := time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)
	var ids []string
	for i := 0; i < 130; i++ {
		ids = append(ids, queueHeartbeat(t, "cs-burst", base.Add(time.Duration(i)*time.Minute)))
	}
	fs.docs[ids[70]] = time.Now()
	// Commit 1 ships batch 1, commit 2 is batch 2 failing on the duplicate,
	// commits 3+ isolate it: fail one of them with a 503.
	fs.failAfter = 5
	fs.fail5xx = 1

	state := newTestRunState()
	tryShip(context.Background(), state)
	tryShip(context.Background(), state)

	if fs.count() != 130 {
		t.Errorf("Firestore holds %d heartbeats, want 130", fs.count())
	}
	if left := outboxFiles(t, ".json"); len(left) != 0 {
		t.Errorf("outbox still holds %d files", len(left))
	}
	if rejected := outboxFiles(t, ".rejected"); len(rejected) != 0 {
		t.Errorf("quarantined %v, want none", rejected)
	}
}

func TestTryShip_PermissionDeniedStillQuarantines(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fs := newFakeFirestore(t)
	base := time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC)
	bad := queueHeartbeat(t, "cs-1", base)
	good := queueHeartbeat(t, "cs-1", base.Add(time.Minute))
	fs.forbidden[bad] = true

	tryShip(context.Background(), newTestRunState())

	if !fs.has(good) {
		t.Error("healthy event not shipped behind the rejected one")
	}
	if rejected := outboxFiles(t, ".rejected"); len(rejected) != 1 || rejected[0] != bad+".json.rejected" {
		t.Errorf("quarantine = %v, want [%s.json.rejected]", rejected, bad)
	}
}

func TestBuildEventDoc_TimeSource(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	state := newTestRunState()
	minute := time.Date(2026, 10, 10, 10, 3, 0, 0, time.UTC)

	hb := queueHeartbeat(t, "cs-1", minute)
	doc, err := buildEventDoc(state, filepath.Join(os.Getenv("HOME"), ".atelier", "outbox", hb+".json"))
	if err != nil {
		t.Fatalf("buildEventDoc(heartbeat): %v", err)
	}
	if doc.ULID != "cs-1_202610101003" || !doc.TS.Equal(minute) || doc.UID != "uid-1" || doc.Host != "host-1" {
		t.Errorf("heartbeat doc = %+v", doc)
	}

	ev := queueEvent(t, "cs-1", "skill:phase-start")
	doc, err = buildEventDoc(state, filepath.Join(os.Getenv("HOME"), ".atelier", "outbox", ev+".json"))
	if err != nil {
		t.Fatalf("buildEventDoc(ulid event): %v", err)
	}
	if want, _ := ulid.Timestamp(ev); !doc.TS.Equal(want) {
		t.Errorf("ulid event ts = %s, want %s", doc.TS, want)
	}

	skewed := minute.Add(42 * time.Second)
	if err := outbox.Write(&outbox.Envelope{ULID: "cs-1_bad", Type: "activity:minute", ClaudeSessionID: "cs-1", Payload: map[string]any{}, TS: &skewed}); err != nil {
		t.Fatalf("write: %v", err)
	}
	bad := filepath.Join(os.Getenv("HOME"), ".atelier", "outbox", "cs-1_bad.json")
	if _, err := buildEventDoc(state, bad); err == nil {
		t.Fatal("a ts off the minute was accepted")
	}
	if _, err := os.Stat(bad + ".corrupt"); err != nil {
		t.Errorf("off-minute file not moved aside: %v", err)
	}
}

func TestHeartbeatReachesFirestoreWithinTenSeconds(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the daemon loops with production intervals")
	}
	t.Setenv("HOME", t.TempDir())
	fs := newFakeFirestore(t)

	parent := filepath.Join(t.TempDir(), "cs-live.jsonl")
	writeJSONL(t, parent, `{"type":"permission-mode","permissionMode":"auto"}`)
	mustRegister(t, "cs-live", parent)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	state := newTestRunState()
	go func() { defer wg.Done(); sessionsManagerLoop(ctx, state) }()
	go func() { defer wg.Done(); shipperLoop(ctx, state) }()
	t.Cleanup(func() { cancel(); wg.Wait() })

	time.Sleep(500 * time.Millisecond)
	dir := strings.TrimSuffix(parent, ".jsonl")
	targets := map[string]string{
		"parent":   parent,
		"subagent": filepath.Join(dir, "subagents", "agent-a.jsonl"),
		"workflow": filepath.Join(dir, "subagents", "workflows", "wf_1", "agent-b.jsonl"),
	}
	written := map[string]time.Time{}
	ids := map[string]string{}
	for i, name := range []string{"parent", "subagent", "workflow"} {
		minute := recentMinute(3 - i)
		appendJSONL(t, targets[name], lineAt("assistant", minute.Add(42*time.Second)))
		written[name] = time.Now()
		ids[name] = transcript.ActivityMinuteID("cs-live", minute)
	}

	if !waitFor(t, 10*time.Second, func() bool {
		return fs.has(ids["parent"]) && fs.has(ids["subagent"]) && fs.has(ids["workflow"])
	}) {
		t.Fatalf("heartbeats not in Firestore within 10 s: parent=%v subagent=%v workflow=%v",
			fs.has(ids["parent"]), fs.has(ids["subagent"]), fs.has(ids["workflow"]))
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	for name, id := range ids {
		if delay := fs.docs[id].Sub(written[name]); delay > 10*time.Second {
			t.Errorf("%s heartbeat took %s, want <= 10 s", name, delay)
		}
	}
}
