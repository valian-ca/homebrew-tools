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
	"errors"
	"strconv"
	"strings"
)

// Type is one of the 11 atelier event types. Validated by IsValid before
// any write to the outbox.
type Type string

const (
	HookSessionStart     Type = "hook:session-start"
	HookUserPromptSubmit Type = "hook:user-prompt-submit"
	HookPreToolUse       Type = "hook:pre-tool-use"
	HookPostToolUse      Type = "hook:post-tool-use"
	HookStop             Type = "hook:stop"
	HookSessionEnd       Type = "hook:session-end"
	SkillPhaseStart      Type = "skill:phase-start"
	SkillPhaseEnd        Type = "skill:phase-end"
	SkillTicketCreated   Type = "skill:ticket-created"
	SkillActivity        Type = "skill:activity"
	SkillShipComplete    Type = "skill:ship-complete"
)

// All returns every valid event type, sorted alphabetically (stable for tests).
func All() []Type {
	return []Type{
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
	}
}

// IsValid reports whether s is one of the recognised event types.
func IsValid(s string) bool {
	for _, t := range All() {
		if string(t) == s {
			return true
		}
	}
	return false
}

// ParsePayload converts a list of "key=value" args into a payload map.
// Each value is parsed with this precedence:
//
//  1. integer (strconv.ParseInt base 10)
//  2. float64 (strconv.ParseFloat)
//  3. boolean ("true" / "false", case-insensitive)
//  4. string (verbatim)
//
// Returns an error on the first malformed arg ("key=" with no value, or no "=").
func ParsePayload(args []string) (map[string]any, error) {
	out := make(map[string]any, len(args))
	for _, raw := range args {
		key, val, ok := strings.Cut(raw, "=")
		if !ok || key == "" {
			return nil, errors.New("invalid --data: must be key=value, got " + strconv.Quote(raw))
		}
		out[key] = parseValue(val)
	}
	return out, nil
}

func parseValue(s string) any {
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	switch strings.ToLower(s) {
	case "true":
		return true
	case "false":
		return false
	}
	return s
}
