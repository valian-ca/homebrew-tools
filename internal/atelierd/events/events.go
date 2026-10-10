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
	HookSessionEnd         Type = "hook:session-end"
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

// ActivityMinute is produced only by the transcript reader and stays out of
// All(), the `atelierd emit` allowlist: Firestore accepts a heartbeat only
// when its doc id is <claudeSessionId>_<UTC minute>, so a hand-emitted one
// with a ULID id would be quarantined.
const ActivityMinute Type = "activity:minute"

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
		HookSessionEnd,
		HookSessionStart,
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
