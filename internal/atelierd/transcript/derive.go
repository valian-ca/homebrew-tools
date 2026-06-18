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
// last-prompt, file-history-snapshot, system, …).
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
	case "ai-title":
		return deriveTitle(state, events.TranscriptAITitle, rec.AiTitle, now, newULID), nil
	case "custom-title":
		return deriveTitle(state, events.TranscriptCustomTitle, rec.CustomTitle, now, newULID), nil
	default:
		return nil, nil
	}
}

func deriveTitle(state *State, eventType events.Type, title string, now Clock, newULID ULIDFn) []*outbox.Envelope {
	// Dedup on the (title, kind) pair. A title line carries no id of its own, so
	// it is the only record type without a natural dedup key — and consume
	// re-reads the whole file from offset 0 whenever it detects truncation. On
	// such a replay every other record is suppressed by LastMsgID / LastPromptID
	// / ClosedToolUseIDs; an unchanged title had nothing and was re-emitted,
	// stamped at re-read time, which advanced the card's lastEventAt and
	// resurfaced shipped/finished cards on the dashboard. Emit only on a real
	// change; a genuine retitle (string or ai→custom) still fires.
	if title == state.LastTitle && string(eventType) == state.LastTitleType {
		return nil
	}
	state.LastTitle = title
	state.LastTitleType = string(eventType)
	return []*outbox.Envelope{{
		ULID:            newULID(),
		Type:            string(eventType),
		ClaudeSessionID: state.ClaudeSessionID,
		Payload:         map[string]any{"title": title},
		CreatedAt:       now(),
	}}
}

func deriveAssistant(state *State, rec Record, now Clock, newULID ULIDFn) []*outbox.Envelope {
	var envs []*outbox.Envelope

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
	toolUseID, isError, hasToolResult := toolResultFromRecord(rec)

	if hasToolResult {
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
			Payload:         postToolUsePayload(toolName, isError),
			CreatedAt:       now(),
		}}
	}

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

func toolResultFromRecord(rec Record) (string, bool, bool) {
	if len(rec.Message.Content) > 0 {
		var blocks []ContentBlock
		if err := json.Unmarshal(rec.Message.Content, &blocks); err == nil {
			for _, blk := range blocks {
				if blk.Type == "tool_result" && blk.ToolUseID != "" {
					return blk.ToolUseID, blk.IsError, true
				}
			}
		}
	}
	if rec.ToolUseResult != nil && rec.ToolUseResult.ToolUseID != "" {
		return rec.ToolUseResult.ToolUseID, rec.ToolUseResult.IsError, true
	}
	return "", false, false
}
