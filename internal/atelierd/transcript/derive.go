package transcript

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/events"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/outbox"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/ulid"
)

// Clock and ULIDFn are injection points so tests can drive the derivation
// deterministically. In production the per-session watcher passes
// time.Now() and ulid.New().
type Clock func() time.Time
type ULIDFn func() string

// Derive consumes one JSONL line, mutates state in-place to record what was
// observed, and returns the envelopes that should be appended to the outbox.
// Returns no envelopes (and no error) for line types we ignore (attachment,
// custom-title, last-prompt, file-history-snapshot, system, …).
//
// Malformed lines are skipped silently with no error — Anthropic occasionally
// writes lines we can't parse (e.g. mid-write truncation, unknown record
// shapes). Returning an error here would stall the whole watcher.
//
// Stop and SessionEnd are NOT derived — see the package docblock for why.
func Derive(state *State, line []byte, now Clock, newULID ULIDFn) ([]*outbox.Envelope, error) {
	if state == nil {
		return nil, errors.New("derive: state is nil")
	}
	if newULID == nil {
		newULID = ulid.New
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if state.OpenToolUseTools == nil {
		state.OpenToolUseTools = map[string]string{}
	}
	if state.ClosedToolUseIDs == nil {
		state.ClosedToolUseIDs = map[string]bool{}
	}

	trimmed := []byte(strings.TrimSpace(string(line)))
	if len(trimmed) == 0 {
		return nil, nil
	}

	var rec Record
	if err := json.Unmarshal(trimmed, &rec); err != nil {
		// Skip unparseable line; the offset still advances so we don't loop.
		return nil, nil
	}

	switch rec.Type {
	case "assistant":
		return deriveAssistant(state, rec, now, newULID), nil
	case "user":
		return deriveUser(state, rec, now, newULID), nil
	default:
		return nil, nil
	}
}

func deriveAssistant(state *State, rec Record, now Clock, newULID ULIDFn) []*outbox.Envelope {
	var envs []*outbox.Envelope

	// hook:assistant-turn — once per unique message.id. The same id appears on
	// every JSONL line for the same turn; we emit only on first sight.
	if rec.Message.ID != "" && rec.Message.ID != state.LastMsgID {
		state.LastMsgID = rec.Message.ID
		envs = append(envs, &outbox.Envelope{
			ULID:            newULID(),
			Type:            string(events.HookAssistantTurn),
			ClaudeSessionID: state.ClaudeSessionID,
			Payload:         assistantTurnPayload(rec.Message.Usage, rec.Message.Model),
			CreatedAt:       now(),
		})
	}

	// hook:pre-tool-use — emit one per tool_use content block on this line.
	// One assistant turn split across multiple lines may carry tool_use on
	// some and other types on others; we emit per-line, deduped per
	// tool_use_id (a tool_use_id is unique within a session).
	if len(rec.Message.Content) > 0 {
		var blocks []ContentBlock
		if err := json.Unmarshal(rec.Message.Content, &blocks); err == nil {
			for _, blk := range blocks {
				if blk.Type != "tool_use" || blk.ID == "" {
					continue
				}
				if _, open := state.OpenToolUseTools[blk.ID]; open {
					continue
				}
				if state.ClosedToolUseIDs[blk.ID] {
					continue
				}
				state.OpenToolUseTools[blk.ID] = blk.Name
				envs = append(envs, &outbox.Envelope{
					ULID:            newULID(),
					Type:            string(events.HookPreToolUse),
					ClaudeSessionID: state.ClaudeSessionID,
					Payload:         preToolUsePayload(blk.Name, blk.Input),
					CreatedAt:       now(),
				})
			}
		}
	}

	return envs
}

func deriveUser(state *State, rec Record, now Clock, newULID ULIDFn) []*outbox.Envelope {
	// user record carrying a tool_result → hook:post-tool-use.
	if rec.ToolUseResult != nil && rec.ToolUseResult.ToolUseID != "" {
		toolUseID := rec.ToolUseResult.ToolUseID
		if state.ClosedToolUseIDs[toolUseID] {
			return nil
		}
		toolName := state.OpenToolUseTools[toolUseID]
		delete(state.OpenToolUseTools, toolUseID)
		state.ClosedToolUseIDs[toolUseID] = true
		return []*outbox.Envelope{{
			ULID:            newULID(),
			Type:            string(events.HookPostToolUse),
			ClaudeSessionID: state.ClaudeSessionID,
			Payload:         postToolUsePayload(toolName, rec.ToolUseResult.IsError),
			CreatedAt:       now(),
		}}
	}

	// user record with no tool_result → user-prompt-submit candidate.
	// Skip system reminders (isMeta:true) and dedup by promptId so a single
	// prompt that fires multiple JSONL lines (initial text + injected
	// attachments) produces one event.
	if rec.IsMeta {
		return nil
	}
	if rec.PromptID == "" || rec.PromptID == state.LastPromptID {
		return nil
	}
	state.LastPromptID = rec.PromptID
	return []*outbox.Envelope{{
		ULID:            newULID(),
		Type:            string(events.HookUserPromptSubmit),
		ClaudeSessionID: state.ClaudeSessionID,
		Payload:         map[string]any{},
		CreatedAt:       now(),
	}}
}
