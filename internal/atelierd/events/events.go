// Package events holds the atelier event taxonomy and the --data payload parser.
//
// SOURCE OF TRUTH for the type list is
//
//	common/schema/src/atelier/event-zod.ts
//
// in the valian-dashboards repo (the EventZod discriminated union). When that
// file changes, this list MUST be updated — the sync is manual + visual diff
// at PR time per VAL-164 stress-test decision. If drift becomes a real-world
// problem, retrofit a CI script that fetches the Zod file and diffs against
// these consts.
package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// VAL-195: ParsePayload writes every `--data key=value` to the outbox as a
// verbatim string. Type coercion (boolean, numeric) is the responsibility of
// consuming Zod schemas in valian-dashboards. The previous implementation
// coerced int/float/bool from the raw string, which made any future string
// field unsafe (e.g. `--data title="2025"` would silently become an int and
// fail Zod validation). The Zod-side `success` field accepts both legacy
// boolean and new string-form via a permanent union.

// Type is one of the 14 atelier event types. Validated by IsValid before
// any write to the outbox.
type Type string

const (
	HookSessionStart      Type = "hook:session-start"
	HookUserPromptSubmit  Type = "hook:user-prompt-submit"
	HookPreToolUse        Type = "hook:pre-tool-use"
	HookPostToolUse       Type = "hook:post-tool-use"
	HookStop              Type = "hook:stop"
	HookSessionEnd        Type = "hook:session-end"
	HookAssistantTurn     Type = "hook:assistant-turn"
	SkillPhaseStart       Type = "skill:phase-start"
	SkillPhaseEnd         Type = "skill:phase-end"
	SkillTicketCreated    Type = "skill:ticket-created"
	SkillActivity         Type = "skill:activity"
	SkillShipComplete     Type = "skill:ship-complete"
	TranscriptAITitle     Type = "transcript:ai-title"
	TranscriptCustomTitle Type = "transcript:custom-title"
)

func All() []Type {
	return []Type{
		HookAssistantTurn,
		HookPostToolUse,
		HookPreToolUse,
		HookSessionEnd,
		HookSessionStart,
		HookStop,
		HookUserPromptSubmit,
		SkillActivity,
		SkillPhaseEnd,
		SkillPhaseStart,
		SkillShipComplete,
		SkillTicketCreated,
		TranscriptAITitle,
		TranscriptCustomTitle,
	}
}

func IsValid(s string) bool {
	for _, t := range All() {
		if string(t) == s {
			return true
		}
	}
	return false
}

// ParsePayload converts a list of "key=value" args into a payload map.
// Values are stored verbatim as strings — see the package docblock above
// for rationale. Returns an error on the first malformed arg ("key=" with
// no value, or no "=").
func ParsePayload(args []string) (map[string]any, error) {
	out := make(map[string]any, len(args))
	for _, raw := range args {
		key, val, ok := strings.Cut(raw, "=")
		if !ok || key == "" {
			return nil, errors.New("invalid --data: must be key=value, got " + strconv.Quote(raw))
		}
		out[key] = val
	}
	return out, nil
}

// ParseJSONPayload is the typed counterpart to ParsePayload: --data stays verbatim-string
// by design (see the VAL-195 docblock above), so nested numeric records like
// an assistant turn's usage breakdown need this explicit opt-in JSON channel.
func ParseJSONPayload(args []string) (map[string]any, error) {
	out := make(map[string]any, len(args))
	for _, raw := range args {
		key, val, ok := strings.Cut(raw, "=")
		if !ok || key == "" {
			return nil, errors.New("invalid --data-json: must be key=<json>, got " + strconv.Quote(raw))
		}
		var parsed any
		if err := json.Unmarshal([]byte(val), &parsed); err != nil {
			return nil, fmt.Errorf("invalid --data-json %s: %w", strconv.Quote(raw), err)
		}
		out[key] = parsed
	}
	return out, nil
}
