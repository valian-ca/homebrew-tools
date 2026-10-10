package transcript

import (
	"strconv"
	"testing"
	"time"
)

func fakeClock(t time.Time) Clock { return func() time.Time { return t } }

func fakeULID() ULIDFn {
	var n int
	return func() string {
		s := "ulid-" + strconv.Itoa(n)
		n++
		return s
	}
}

func newTestState() *State {
	return &State{ClaudeSessionID: "cs-test", JSONLPath: "/tmp/cs-test.jsonl"}
}

func TestActivityMinuteID_MatchesDashboardKey(t *testing.T) {
	t.Parallel()
	cases := []struct {
		session string
		minute  time.Time
		want    string
	}{
		{"session-1", time.Date(2026, 4, 25, 21, 0, 0, 0, time.UTC), "session-1_202604252100"},
		{"cs-abc", time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC), "cs-abc_202601020304"},
		{"cs-tz", time.Date(2026, 10, 10, 6, 3, 0, 0, time.FixedZone("EDT", -4*3600)), "cs-tz_202610101003"},
	}
	for _, c := range cases {
		if got := ActivityMinuteID(c.session, c.minute); got != c.want {
			t.Errorf("ActivityMinuteID(%q, %s) = %q, want %q", c.session, c.minute, got, c.want)
		}
	}
}

func TestDerive_HeartbeatUsesTheLineMinuteNotTheReadTime(t *testing.T) {
	t.Parallel()
	state := newTestState()
	readAt := fakeClock(time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC))

	envs := Derive(state, []byte(`{"type":"assistant","timestamp":"2026-10-10T10:03:42.123Z"}`), readAt, fakeULID())

	if len(envs) != 1 {
		t.Fatalf("want 1 envelope, got %d", len(envs))
	}
	env := envs[0]
	want := time.Date(2026, 10, 10, 10, 3, 0, 0, time.UTC)
	if env.Type != "activity:minute" {
		t.Errorf("Type = %q, want activity:minute", env.Type)
	}
	if env.ULID != "cs-test_202610101003" {
		t.Errorf("ULID = %q, want cs-test_202610101003", env.ULID)
	}
	if env.TS == nil || !env.TS.Equal(want) {
		t.Errorf("TS = %v, want %s", env.TS, want)
	}
	if env.ClaudeSessionID != "cs-test" {
		t.Errorf("ClaudeSessionID = %q, want cs-test", env.ClaudeSessionID)
	}
	if len(env.Payload) != 0 {
		t.Errorf("Payload = %v, want empty", env.Payload)
	}
}

func TestDerive_OneHeartbeatPerMinute(t *testing.T) {
	t.Parallel()
	state := newTestState()
	now := fakeClock(time.Now().UTC())
	lines := []string{
		`{"type":"user","timestamp":"2026-10-10T10:03:00.000Z"}`,
		`{"type":"assistant","timestamp":"2026-10-10T10:03:59.999Z"}`,
		`{"type":"attachment","timestamp":"2026-10-10T10:04:00.000Z"}`,
		`{"type":"system","timestamp":"2026-10-10T10:03:30.000Z"}`,
	}
	var ids []string
	for _, l := range lines {
		for _, env := range Derive(state, []byte(l), now, fakeULID()) {
			ids = append(ids, env.ULID)
		}
	}
	if len(ids) != 2 || ids[0] != "cs-test_202610101003" || ids[1] != "cs-test_202610101004" {
		t.Fatalf("heartbeats = %v, want [cs-test_202610101003 cs-test_202610101004]", ids)
	}
}

func TestDerive_LinesWithoutUsableTimestampEmitNothing(t *testing.T) {
	t.Parallel()
	now := fakeClock(time.Now().UTC())
	for _, l := range []string{
		`{"type":"permission-mode","permissionMode":"auto"}`,
		`{"type":"last-prompt","lastPrompt":"x"}`,
		`{"type":"user","timestamp":"yesterday"}`,
		`{"type":"user","timestamp":42}`,
		`not json {{`,
		``,
		`   `,
	} {
		if envs := Derive(newTestState(), []byte(l), now, fakeULID()); len(envs) != 0 {
			t.Errorf("line %q: want no envelope, got %#v", l, envs)
		}
	}
}

func TestDerive_DetailedTypesAreNeverDerived(t *testing.T) {
	t.Parallel()
	state := newTestState()
	now := fakeClock(time.Now().UTC())
	lines := []string{
		`{"type":"user","timestamp":"2026-10-10T10:00:01Z","promptId":"p1","message":{"role":"user","content":"hi"}}`,
		`{"type":"assistant","timestamp":"2026-10-10T10:01:01Z","message":{"id":"m1","usage":{"input_tokens":1},"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}}`,
		`{"type":"user","timestamp":"2026-10-10T10:02:01Z","message":{"content":[{"type":"tool_result","tool_use_id":"t1"}]}}`,
	}
	for _, l := range lines {
		for _, env := range Derive(state, []byte(l), now, fakeULID()) {
			if env.Type != "activity:minute" {
				t.Errorf("line %q derived %q, want only activity:minute", l, env.Type)
			}
		}
	}
}

func TestDerive_TitleEmitsOnChangeOnly(t *testing.T) {
	t.Parallel()
	now := fakeClock(time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC))
	state := newTestState()

	line := []byte(`{"type":"custom-title","customTitle":"Forge FLEX-167"}`)
	envs := Derive(state, line, now, fakeULID())
	if len(envs) != 1 || envs[0].Type != "transcript:custom-title" || envs[0].Payload["title"] != "Forge FLEX-167" {
		t.Fatalf("first sight: want one transcript:custom-title, got %#v", envs)
	}
	if envs[0].TS != nil {
		t.Errorf("title TS = %v, want nil (ts comes from the ULID)", envs[0].TS)
	}
	if again := Derive(state, line, now, fakeULID()); len(again) != 0 {
		t.Errorf("unchanged title re-emitted: %#v", again)
	}
	if retitle := Derive(state, []byte(`{"type":"custom-title","customTitle":"Forge FLEX-200"}`), now, fakeULID()); len(retitle) != 1 {
		t.Errorf("changed title should emit, got %#v", retitle)
	}
	kind := Derive(state, []byte(`{"type":"ai-title","aiTitle":"Forge FLEX-200"}`), now, fakeULID())
	if len(kind) != 1 || kind[0].Type != "transcript:ai-title" {
		t.Errorf("kind change should emit a transcript:ai-title, got %#v", kind)
	}
}

func TestDerive_TimestampedTitleAlsoBeats(t *testing.T) {
	t.Parallel()
	state := newTestState()
	envs := Derive(state, []byte(`{"type":"ai-title","aiTitle":"T","timestamp":"2026-10-10T10:03:42Z"}`), fakeClock(time.Now()), fakeULID())
	if len(envs) != 2 || envs[0].Type != "transcript:ai-title" || envs[1].Type != "activity:minute" {
		t.Fatalf("want [ai-title activity:minute], got %#v", envs)
	}
}

func TestDerive_ForgottenMinuteCanEmitAgain(t *testing.T) {
	t.Parallel()
	state := newTestState()
	now := fakeClock(time.Now())
	line := []byte(`{"type":"user","timestamp":"2026-10-10T10:03:42Z"}`)
	envs := Derive(state, line, now, fakeULID())
	if len(envs) != 1 {
		t.Fatalf("want 1 heartbeat, got %d", len(envs))
	}
	state.ForgetMinute(*envs[0].TS)
	if again := Derive(state, line, now, fakeULID()); len(again) != 1 {
		t.Fatalf("forgotten minute: want 1 heartbeat, got %d", len(again))
	}
}

func TestDerive_OffsetTimestampMapsToItsUTCMinute(t *testing.T) {
	t.Parallel()
	envs := Derive(newTestState(), []byte(`{"type":"user","timestamp":"2026-10-10T12:03:42.5+02:00"}`), fakeClock(time.Now()), fakeULID())
	if len(envs) != 1 || envs[0].ULID != "cs-test_202610101003" {
		t.Fatalf("envelopes = %#v, want cs-test_202610101003", envs)
	}
}
