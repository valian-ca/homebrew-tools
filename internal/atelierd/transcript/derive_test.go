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

	line2 := []byte(`{"type":"assistant","message":{"id":"msg_A","model":"claude-opus-4-7","usage":{"input_tokens":10,"output_tokens":5},"content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}]}}`)
	envs2, err := Derive(state, line2, now, mk)
	if err != nil {
		t.Fatalf("line2 derive error: %v", err)
	}
	if len(envs2) != 1 || envs2[0].Type != "hook:pre-tool-use" {
		t.Fatalf("line2: want one hook:pre-tool-use, got %#v", envs2)
	}

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

	line := []byte(`{"type":"assistant","message":{"id":"msg_X","content":[{"type":"tool_use","id":"toolu_42","name":"Edit","input":{"file_path":"/a/b.go","command":"ignored","pattern":"foo","query":"bar","url":"https://e.x","description":"d","random_extra":true}}]}}`)
	envs, err := Derive(state, line, now, fakeULID())
	if err != nil {
		t.Fatalf("derive error: %v", err)
	}
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
	}
	for k, v := range want {
		if pre.Payload[k] != v {
			t.Errorf("payload[%q] = %v, want %v", k, pre.Payload[k], v)
		}
	}
	if _, ok := pre.Payload["random_extra"]; ok {
		t.Errorf("random_extra leaked into payload")
	}
	if _, ok := pre.Payload["skillName"]; ok {
		t.Errorf("skillName must not be forwarded for non-Skill tools (dashboard contract)")
	}
}

func TestDerive_PreToolUsePayloadForwardsSkillNameOnlyForSkillTool(t *testing.T) {
	now := fakeClock(time.Now().UTC())
	state := newTestState()

	line := []byte(`{"type":"assistant","message":{"id":"msg_S","content":[{"type":"tool_use","id":"toolu_skill","name":"Skill","input":{"skill":"valian:br","args":"some args"}}]}}`)
	envs, err := Derive(state, line, now, fakeULID())
	if err != nil {
		t.Fatalf("derive error: %v", err)
	}
	if len(envs) != 2 {
		t.Fatalf("want 2 envelopes, got %d", len(envs))
	}
	pre := envs[1]
	if pre.Type != "hook:pre-tool-use" {
		t.Fatalf("want hook:pre-tool-use, got %s", pre.Type)
	}
	if pre.Payload["tool"] != "Skill" {
		t.Errorf("payload.tool = %v, want Skill", pre.Payload["tool"])
	}
	if pre.Payload["skillName"] != "valian:br" {
		t.Errorf("payload.skillName = %v, want valian:br", pre.Payload["skillName"])
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

func TestDerive_PostToolUseFromContentBlock(t *testing.T) {
	// Claude Code 2.x carries tool_use_id and is_error on a tool_result
	// content block inside message.content. The top-level toolUseResult is
	// a per-tool wrapper of varying shape (stdout/stderr for Bash, filenames
	// for Glob, …) and never carries tool_use_id — so deriveUser must read
	// the content block, not the legacy top-level field, or every real
	// session loses every hook:post-tool-use event.
	now := fakeClock(time.Now().UTC())

	state := newTestState()
	state.OpenToolUseTools["toolu_real"] = "Bash"
	line := []byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_real","content":"ok"}]},"toolUseResult":{"stdout":"ok","stderr":""}}`)
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
		t.Errorf("absent is_error must read as success=\"true\", got %v", envs[0].Payload["success"])
	}
	if _, still := state.OpenToolUseTools["toolu_real"]; still {
		t.Errorf("OpenToolUseTools should have pruned toolu_real after post fired")
	}

	state.OpenToolUseTools["toolu_err"] = "Edit"
	line2 := []byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_err","content":"boom","is_error":true}]}}`)
	envs2, _ := Derive(state, line2, now, fakeULID())
	if len(envs2) != 1 || envs2[0].Payload["success"] != "false" {
		t.Errorf("is_error:true must read as success=\"false\", got %#v", envs2)
	}
}

func TestDerive_PostToolUseFromUserToolResult(t *testing.T) {
	now := fakeClock(time.Now().UTC())
	state := newTestState()
	state.OpenToolUseTools["toolu_99"] = "Bash"

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

	line1 := []byte(`{"type":"user","promptId":"p1","message":{"role":"user","content":"hello"}}`)
	envs1, _ := Derive(state, line1, now, fakeULID())
	if len(envs1) != 1 || envs1[0].Type != "hook:user-prompt-submit" {
		t.Fatalf("line1: want hook:user-prompt-submit, got %#v", envs1)
	}

	line2 := []byte(`{"type":"user","promptId":"p1","message":{"role":"user","content":"more text from same prompt"}}`)
	envs2, _ := Derive(state, line2, now, fakeULID())
	if len(envs2) != 0 {
		t.Errorf("line2: want no events for same promptId, got %#v", envs2)
	}

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
	for _, typ := range []string{"attachment", "last-prompt", "file-history-snapshot", "system"} {
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

func TestDerive_AiTitleEmitsOneEnvelope(t *testing.T) {
	now := fakeClock(time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC))
	state := newTestState()

	line := []byte(`{"type":"ai-title","aiTitle":"My Session Title"}`)
	envs, err := Derive(state, line, now, fakeULID())
	if err != nil {
		t.Fatalf("derive error: %v", err)
	}
	if len(envs) != 1 {
		t.Fatalf("want 1 envelope, got %d", len(envs))
	}
	if envs[0].Type != "transcript:ai-title" {
		t.Errorf("Type = %q, want transcript:ai-title", envs[0].Type)
	}
	if envs[0].ClaudeSessionID != "cs-test" {
		t.Errorf("ClaudeSessionID = %q, want cs-test", envs[0].ClaudeSessionID)
	}
	if envs[0].Payload["title"] != "My Session Title" {
		t.Errorf("Payload[title] = %v, want My Session Title", envs[0].Payload["title"])
	}
}

func TestDerive_CustomTitleEmitsOneEnvelope(t *testing.T) {
	now := fakeClock(time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC))
	state := newTestState()

	line := []byte(`{"type":"custom-title","customTitle":"User chosen name"}`)
	envs, err := Derive(state, line, now, fakeULID())
	if err != nil {
		t.Fatalf("derive error: %v", err)
	}
	if len(envs) != 1 {
		t.Fatalf("want 1 envelope, got %d", len(envs))
	}
	if envs[0].Type != "transcript:custom-title" {
		t.Errorf("Type = %q, want transcript:custom-title", envs[0].Type)
	}
	if envs[0].Payload["title"] != "User chosen name" {
		t.Errorf("Payload[title] = %v, want User chosen name", envs[0].Payload["title"])
	}
}

// Empty title still emits when it is a *change* — a deliberate clear should
// reach Firestore rather than being silently absorbed. (Each case below flips
// the title kind, so every line is a genuine change; consecutive identical
// titles are deduped, see TestDerive_TitleDedupOnReplay.)
func TestDerive_EmptyTitleStillEmitsEnvelope(t *testing.T) {
	now := fakeClock(time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC))
	state := newTestState()

	for _, c := range []struct {
		typ      string
		payload  string
		wantType string
	}{
		{"ai-title", `{"type":"ai-title","aiTitle":""}`, "transcript:ai-title"},
		{"custom-title", `{"type":"custom-title","customTitle":""}`, "transcript:custom-title"},
		{"ai-title", `{"type":"ai-title"}`, "transcript:ai-title"},
		{"custom-title", `{"type":"custom-title"}`, "transcript:custom-title"},
	} {
		envs, err := Derive(state, []byte(c.payload), now, fakeULID())
		if err != nil {
			t.Errorf("%s: derive error: %v", c.typ, err)
			continue
		}
		if len(envs) != 1 {
			t.Errorf("%s: want 1 envelope, got %d", c.typ, len(envs))
			continue
		}
		if envs[0].Type != c.wantType {
			t.Errorf("%s: Type = %q, want %q", c.typ, envs[0].Type, c.wantType)
		}
		if envs[0].Payload["title"] != "" {
			t.Errorf("%s: Payload[title] = %v, want empty string", c.typ, envs[0].Payload["title"])
		}
	}
}

func TestDerive_TitleDedupOnReplay(t *testing.T) {
	// Reproduces the 2026-06-18 burst: when consume detects the JSONL as
	// truncated it resets the offset to 0 and re-reads every line. An unchanged
	// title must NOT re-emit (else its event is stamped at re-read time and
	// resurrects shipped cards). A genuine retitle must still fire.
	now := fakeClock(time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC))
	state := newTestState()

	line := []byte(`{"type":"custom-title","customTitle":"Forge FLEX-167"}`)
	envs1, _ := Derive(state, line, now, fakeULID())
	if len(envs1) != 1 || envs1[0].Type != "transcript:custom-title" {
		t.Fatalf("first sight: want one transcript:custom-title, got %#v", envs1)
	}
	if state.LastTitle != "Forge FLEX-167" || state.LastTitleType != "transcript:custom-title" {
		t.Fatalf("state not recorded: %q / %q", state.LastTitle, state.LastTitleType)
	}

	// Replay the same line (offset-reset re-read). No re-emit.
	envs2, _ := Derive(state, line, now, fakeULID())
	if len(envs2) != 0 {
		t.Errorf("unchanged title re-emitted on replay: %#v", envs2)
	}

	// A real retitle still fires.
	envs3, _ := Derive(state, []byte(`{"type":"custom-title","customTitle":"Forge FLEX-200"}`), now, fakeULID())
	if len(envs3) != 1 {
		t.Errorf("changed title should emit, got %#v", envs3)
	}

	// Same string but a different kind (ai vs custom) is also a change.
	envs4, _ := Derive(state, []byte(`{"type":"ai-title","aiTitle":"Forge FLEX-200"}`), now, fakeULID())
	if len(envs4) != 1 || envs4[0].Type != "transcript:ai-title" {
		t.Errorf("kind change should emit a transcript:ai-title, got %#v", envs4)
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

func TestDerive_SubagentEnvelopesCarryParentClaudeSessionID(t *testing.T) {
	// Every envelope must carry the parent's ClaudeSessionID — that ID is
	// the pivot aggregatePhaseRun on the backend folds subagent tokens into
	// the parent's phaseRun on, without any backend-side change.
	now := fakeClock(time.Now().UTC())
	state := &State{
		ClaudeSessionID:  "cs-parent",
		WatcherKey:       "cs-parent/subagents/agent-x",
		JSONLPath:        "/tmp/cs-parent/subagents/agent-x.jsonl",
		OpenToolUseTools: map[string]string{},
		ClosedToolUseIDs: map[string]bool{},
	}

	line := []byte(`{"type":"assistant","message":{"id":"msg_haiku","model":"claude-haiku-4-5-20251001","usage":{"input_tokens":7,"output_tokens":2,"cache_read_input_tokens":3},"content":[{"type":"text","text":"…"}]}}`)
	envs, err := Derive(state, line, now, fakeULID())
	if err != nil {
		t.Fatalf("derive error: %v", err)
	}
	if len(envs) != 1 {
		t.Fatalf("want 1 envelope, got %d", len(envs))
	}
	if envs[0].ClaudeSessionID != "cs-parent" {
		t.Errorf("envelope ClaudeSessionID = %q, want cs-parent (parent's id)", envs[0].ClaudeSessionID)
	}
	if envs[0].Payload["model"] != "claude-haiku-4-5-20251001" {
		t.Errorf("envelope model = %v, want claude-haiku-4-5-20251001 (subagent's actual model)", envs[0].Payload["model"])
	}
}

func TestDerive_RestartReplayNoDuplication(t *testing.T) {
	// kill -9 mid-session, restart, no dup. Simulated by re-replaying the same
	// lines against the same state and asserting no new envelopes for
	// already-emitted msg_id / promptId / tool_use_id.
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
	if totalFirstPass != 4 {
		t.Fatalf("first pass: want 4 envelopes, got %d", totalFirstPass)
	}

	totalReplay := 0
	for _, l := range lines {
		envs, _ := Derive(state, l, now, fakeULID())
		totalReplay += len(envs)
	}
	if totalReplay != 0 {
		t.Errorf("replay: want 0 dup envelopes, got %d", totalReplay)
	}
}
