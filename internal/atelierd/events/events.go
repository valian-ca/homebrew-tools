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

type Type string

// Keep this taxonomy in sync with valian-dashboards/common/schema/src/atelier/event-zod.ts.
const (
	ForgeCampaignSaved     Type = "forge:campaign-saved"
	ForgePass              Type = "forge:pass"
	ForgeReportLinked      Type = "forge:report-linked"
	ForgeRunStart          Type = "forge:run-start"
	ForgeTestplanPublished Type = "forge:testplan-published"
	ForgeWaveClose         Type = "forge:wave-close"
	ForgeWaveOpen          Type = "forge:wave-open"
	HookSessionStart       Type = "hook:session-start"
	HookUserPromptSubmit   Type = "hook:user-prompt-submit"
	HookPreToolUse         Type = "hook:pre-tool-use"
	HookPostToolUse        Type = "hook:post-tool-use"
	HookStop               Type = "hook:stop"
	HookSessionEnd         Type = "hook:session-end"
	HookAssistantTurn      Type = "hook:assistant-turn"
	SkillPhaseStart        Type = "skill:phase-start"
	SkillPhaseEnd          Type = "skill:phase-end"
	SkillTicketCreated     Type = "skill:ticket-created"
	SkillActivity          Type = "skill:activity"
	SkillShipComplete      Type = "skill:ship-complete"
	TranscriptAITitle      Type = "transcript:ai-title"
	TranscriptCustomTitle  Type = "transcript:custom-title"
)

func All() []Type {
	return []Type{
		ForgeCampaignSaved,
		ForgePass,
		ForgeReportLinked,
		ForgeRunStart,
		ForgeTestplanPublished,
		ForgeWaveClose,
		ForgeWaveOpen,
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
// Values are stored verbatim as strings — see the VAL-195 docblock above
// for rationale.
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
// by design (see the VAL-195 docblock above), so nested or numeric payload
// fields need this explicit opt-in JSON channel.
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
