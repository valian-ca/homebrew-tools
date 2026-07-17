package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type Type string

// Keep this taxonomy in sync with valian-dashboards/common/schema/src/atelier/event-zod.ts.
const (
	ForgeCampaignSaved     Type = "forge:campaign-saved"
	ForgeOutcomeRecorded   Type = "forge:outcome-recorded"
	ForgePass              Type = "forge:pass"
	ForgeReportLinked      Type = "forge:report-linked"
	ForgeRunStart          Type = "forge:run-start"
	ForgeTestplanLinked    Type = "forge:testplan-linked"
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
	ShipCIRound            Type = "ship:ci-round"
	ShipPRLinked           Type = "ship:pr-linked"
	ShipRunStart           Type = "ship:run-start"
	ShipStep               Type = "ship:step"
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
		ForgeOutcomeRecorded,
		ForgePass,
		ForgeReportLinked,
		ForgeRunStart,
		ForgeTestplanLinked,
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
		ShipCIRound,
		ShipPRLinked,
		ShipRunStart,
		ShipStep,
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

// ParseJSONPayload is the explicit opt-in channel for typed JSON fields;
// ParsePayload keeps --data values as verbatim strings.
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
