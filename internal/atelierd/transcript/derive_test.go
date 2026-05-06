package transcript

import (
	"bytes"
	"encoding/json"
	"strconv"
	"testing"
	"time"
)

// fakeClock returns a fixed UTC instant. Tests that need monotonically
// advancing timestamps should compose their own closure.
func fakeClock(t time.Time) Clock { return func() time.Time { return t } }

// fakeULID returns successive ULID-shaped strings ulid-0, ulid-1, … so tests
// can match envelopes by stable identifier.
func fakeULID() ULIDFn {
	var n int
	return func() string {
		s := "ulid-" + strconv.Itoa(n)
		n++
		return s
	}
}

func newTestState() *State {
	return &State{
		ClaudeSessionID:  "cs-test",
		JSONLPath:        "/tmp/test.jsonl",
		OpenToolUseTools: map[string]string{},
		ClosedToolUseIDs: map[string]bool{},
	}
}

func TestDerive_AssistantTurnEmittedOncePerMessageID(t *testing.T) {
	now := fakeClock(time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC))
	mk := fakeULID()
	state := newTestState()

	// First line of msg_A — assistant with thinking content.
	line1 := []byte(`{"type":"assistant","message":{"id":"msg_A","model":"claude-opus-4-7","usage":{"input_tokens":10,"output_tokens":5},"content":[{"type":"thinking","thinking":"…"}]}}`)
	envs1, err := Derive(state, line1, now, mk)
	if err != nil {
		t.Fatalf("line1 derive error: %v", err)
	}
	if len(envs1) != 1 || envs1[0].Type != "hook:assistant-turn" {
		t.Fatalf("line1: want one hook:assistant-turn, got %#v", envs1)
	}
	if state.LastMsgID != "msg_A" {
		t.Errorf("state.LastMsgID = %q, want msg_A", state.LastMsgID)
	}

	// Second line of msg_A — same turn, tool_use content. Should NOT re-emit
	// hook:assistant-turn but SHOULD emit hook:pre-tool-use.
	line2 := []byte(`{"type":"assistant","message":{"id":"msg_A","model":"claude-opus-4-7","usage":{"input_tokens":10,"output_tokens":5},"content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}]}}`)
	envs2, err := Derive(state, line2, now, mk)
	if err != nil {
		t.Fatalf("line2 derive error: %v", err)
	}
	if len(envs2) != 1 || envs2[0].Type != "hook:pre-tool-use" {
		t.Fatalf("line2: want one hook:pre-tool-use, got %#v", envs2)
	}

	// Third line — new msg_B. Re-emit hook:assistant-turn.
	line3 := []byte(`{"type":"assistant","message":{"id":"msg_B","model":"claude-opus-4-7","usage":{"input_tokens":12,"output_tokens":3},"content":[{"type":"text","text":"…"}]}}`)
	envs3, err := Derive(state, line3, now, mk)
	if err != nil {
		t.Fatalf("line3 derive error: %v", err)
	}
	if len(envs3) != 1 || envs3[0].Type != "hook:assistant-turn" {
		t.Fatalf("line3: want one hook:assistant-turn for msg_B, got %#v", envs3)
	}
}

func TestDerive_PreToolUsePayloadMatchesBashContract(t *testing.T) {
	now := fakeClock(time.Now().UTC())
	state := newTestState()

	line := []byte(`{"type":"assistant","message":{"id":"msg_X","content":[{"type":"tool_use","id":"toolu_42","name":"Edit","input":{"file_path":"/a/b.go","command":"ignored","pattern":"foo","query":"bar","url":"https://e.x","description":"d","skill":"s","random_extra":true}}]}}`)
	envs, err := Derive(state, line, now, fakeULID())
	if err != nil {
		t.Fatalf("derive error: %v", err)
	}
	// Two events: assistant-turn + pre-tool-use.
	if len(envs) != 2 {
		t.Fatalf("want 2 envelopes, got %d", len(envs))
	}
	pre := envs[1]
	if pre.Type != "hook:pre-tool-use" {
		t.Fatalf("want hook:pre-tool-use, got %s", pre.Type)
	}
	want := map[string]any{
		"tool":        "Edit",
		"filePath":    "/a/b.go",
		"command":     "ignored",
		"pattern":     "foo",
		"query":       "bar",
		"url":         "https://e.x",
		"description": "d",
		"skillName":   "s",
	}
	for k, v := range want {
		if pre.Payload[k] != v {
			t.Errorf("payload[%q] = %v, want %v", k, pre.Payload[k], v)
		}
	}
	// random_extra must not be forwarded — the bash hook never did.
	if _, ok := pre.Payload["random_extra"]; ok {
		t.Errorf("random_extra leaked into payload")
	}
}

func TestDerive_PreToolUseDedupByToolUseID(t *testing.T) {
	now := fakeClock(time.Now().UTC())
	state := newTestState()
	line := []byte(`{"type":"assistant","message":{"id":"msg_dup","content":[{"type":"tool_use","id":"toolu_dup","name":"Bash","input":{"command":"ls"}}]}}`)
	if _, err := Derive(state, line, now, fakeULID()); err != nil {
		t.Fatalf("first derive error: %v", err)
	}
	// Re-emit the same tool_use_id (e.g. atelierd resumes from before this
	// line). The pre-tool-use must NOT fire again. The assistant-turn was
	// already emitted on first sight (LastMsgID gates that), so we only
	// check that no new envelopes are produced for this duplicate read.
	envs, err := Derive(state, line, now, fakeULID())
	if err != nil {
		t.Fatalf("second derive error: %v", err)
	}
	for _, e := range envs {
		if e.Type == "hook:pre-tool-use" {
			t.Errorf("pre-tool-use re-emitted for known tool_use_id")
		}
	}
}

func TestDerive_PostToolUseFromUserToolResult(t *testing.T) {
	now := fakeClock(time.Now().UTC())
	state := newTestState()
	state.OpenToolUseTools["toolu_99"] = "Bash"

	// is_error: false → success: "true"
	line := []byte(`{"type":"user","toolUseResult":{"tool_use_id":"toolu_99","is_error":false}}`)
	envs, err := Derive(state, line, now, fakeULID())
	if err != nil {
		t.Fatalf("derive error: %v", err)
	}
	if len(envs) != 1 || envs[0].Type != "hook:post-tool-use" {
		t.Fatalf("want hook:post-tool-use, got %#v", envs)
	}
	if envs[0].Payload["tool"] != "Bash" {
		t.Errorf("payload.tool = %v, want Bash", envs[0].Payload["tool"])
	}
	if envs[0].Payload["success"] != "true" {
		t.Errorf("payload.success = %v, want \"true\"", envs[0].Payload["success"])
	}
	if _, still := state.OpenToolUseTools["toolu_99"]; still {
		t.Errorf("OpenToolUseTools should have pruned toolu_99 after post fired")
	}
	if !state.ClosedToolUseIDs["toolu_99"] {
		t.Errorf("ClosedToolUseIDs should have recorded toolu_99")
	}

	// is_error: true → success: "false"
	state.OpenToolUseTools["toolu_100"] = "Edit"
	line2 := []byte(`{"type":"user","toolUseResult":{"tool_use_id":"toolu_100","is_error":true}}`)
	envs2, _ := Derive(state, line2, now, fakeULID())
	if envs2[0].Payload["success"] != "false" {
		t.Errorf("payload.success = %v, want \"false\"", envs2[0].Payload["success"])
	}
}

func TestDerive_UserPromptSubmitDedupByPromptID(t *testing.T) {
	now := fakeClock(time.Now().UTC())
	state := newTestState()

	// First user record with promptId=p1 — emit.
	line1 := []byte(`{"type":"user","promptId":"p1","message":{"role":"user","content":"hello"}}`)
	envs1, _ := Derive(state, line1, now, fakeULID())
	if len(envs1) != 1 || envs1[0].Type != "hook:user-prompt-submit" {
		t.Fatalf("line1: want hook:user-prompt-submit, got %#v", envs1)
	}

	// Same promptId — DO NOT re-emit.
	line2 := []byte(`{"type":"user","promptId":"p1","message":{"role":"user","content":"more text from same prompt"}}`)
	envs2, _ := Derive(state, line2, now, fakeULID())
	if len(envs2) != 0 {
		t.Errorf("line2: want no events for same promptId, got %#v", envs2)
	}

	// New promptId — emit again.
	line3 := []byte(`{"type":"user","promptId":"p2","message":{"role":"user","content":"new prompt"}}`)
	envs3, _ := Derive(state, line3, now, fakeULID())
	if len(envs3) != 1 {
		t.Fatalf("line3: want 1 event for new promptId, got %#v", envs3)
	}
}

func TestDerive_MetaUserRecordSkipped(t *testing.T) {
	now := fakeClock(time.Now().UTC())
	state := newTestState()
	line := []byte(`{"type":"user","promptId":"p3","isMeta":true,"message":{"role":"user","content":"system reminder"}}`)
	envs, _ := Derive(state, line, now, fakeULID())
	if len(envs) != 0 {
		t.Errorf("isMeta:true should not emit, got %#v", envs)
	}
}

func TestDerive_IgnoredRecordTypes(t *testing.T) {
	now := fakeClock(time.Now().UTC())
	state := newTestState()
	for _, typ := range []string{"attachment", "custom-title", "last-prompt", "file-history-snapshot", "system"} {
		line := []byte(`{"type":"` + typ + `"}`)
		envs, err := Derive(state, line, now, fakeULID())
		if err != nil {
			t.Errorf("%s: unexpected error %v", typ, err)
		}
		if len(envs) != 0 {
			t.Errorf("%s: want 0 envelopes, got %d", typ, len(envs))
		}
	}
}

func TestDerive_MalformedLineSkipped(t *testing.T) {
	now := fakeClock(time.Now().UTC())
	state := newTestState()
	envs, err := Derive(state, []byte("not json {{"), now, fakeULID())
	if err != nil {
		t.Errorf("malformed line should not return error, got %v", err)
	}
	if len(envs) != 0 {
		t.Errorf("malformed line should produce no envelopes")
	}
}

func TestDerive_EmptyLineSkipped(t *testing.T) {
	now := fakeClock(time.Now().UTC())
	state := newTestState()
	for _, b := range [][]byte{nil, {}, []byte("   "), []byte("\n")} {
		envs, _ := Derive(state, b, now, fakeULID())
		if len(envs) != 0 {
			t.Errorf("empty line should be no-op, got %d envelopes", len(envs))
		}
	}
}

func TestDerive_UnknownUsageFieldsPreservedByteForByte(t *testing.T) {
	// VAL-201 AC 5 — the most important test: unknown nested fields in usage
	// must flow through to the outbox payload unchanged.
	now := fakeClock(time.Now().UTC())
	state := newTestState()
	rawLine := []byte(`{"type":"assistant","message":{"id":"msg_unk","model":"claude-future-7","usage":{"input_tokens":1,"custom_field":42,"custom_nested":{"foo":7,"bar":["a","b"]}},"content":[{"type":"text","text":"…"}]}}`)
	envs, err := Derive(state, rawLine, now, fakeULID())
	if err != nil {
		t.Fatalf("derive error: %v", err)
	}
	if len(envs) != 1 {
		t.Fatalf("want 1 envelope, got %d", len(envs))
	}
	got, _ := json.Marshal(envs[0].Payload["usage"])
	want := []byte(`{"input_tokens":1,"custom_field":42,"custom_nested":{"foo":7,"bar":["a","b"]}}`)
	if !bytes.Equal(got, want) {
		t.Errorf("usage was rewritten:\n  got  %s\n  want %s", got, want)
	}
	if envs[0].Payload["model"] != "claude-future-7" {
		t.Errorf("model = %v, want claude-future-7", envs[0].Payload["model"])
	}
}

func TestDerive_RestartReplayNoDuplication(t *testing.T) {
	// VAL-201 AC 4 — kill -9 mid-session, restart, no dup. Simulated by
	// re-replaying the same lines against the same state and asserting no new
	// envelopes for already-emitted msg_id / promptId / tool_use_id.
	now := fakeClock(time.Now().UTC())
	state := newTestState()

	lines := [][]byte{
		[]byte(`{"type":"user","promptId":"p1","message":{"role":"user","content":"hi"}}`),
		[]byte(`{"type":"assistant","message":{"id":"msg_1","model":"m","usage":{"input_tokens":1},"content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}]}}`),
		[]byte(`{"type":"user","toolUseResult":{"tool_use_id":"toolu_1","is_error":false}}`),
	}

	totalFirstPass := 0
	for _, l := range lines {
		envs, _ := Derive(state, l, now, fakeULID())
		totalFirstPass += len(envs)
	}
	// Expect: 1 user-prompt-submit + 1 assistant-turn + 1 pre-tool-use + 1 post-tool-use = 4.
	if totalFirstPass != 4 {
		t.Fatalf("first pass: want 4 envelopes, got %d", totalFirstPass)
	}

	// Replay (atelierd resumed from a stale offset before kill).
	totalReplay := 0
	for _, l := range lines {
		envs, _ := Derive(state, l, now, fakeULID())
		totalReplay += len(envs)
	}
	if totalReplay != 0 {
		t.Errorf("replay: want 0 dup envelopes, got %d", totalReplay)
	}
}
