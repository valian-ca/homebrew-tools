package transcript

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/events"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/outbox"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/ulid"
)

type Clock func() time.Time
type ULIDFn func() string

const minuteKeyLayout = "200601021504"

// ActivityMinuteID mirrors activityMinuteId in valian-dashboards
// (common/schema/src/atelier/activity-minute.ts): the Firestore rules accept a
// heartbeat only when its doc id equals this key, which also makes every
// re-derivation of the same minute collide on the same document.
func ActivityMinuteID(claudeSessionID string, minute time.Time) string {
	return claudeSessionID + "_" + minuteKey(minute)
}

func minuteKey(minute time.Time) string {
	return minute.UTC().Format(minuteKeyLayout)
}

func Derive(state *State, line []byte, now Clock, newULID ULIDFn) []*outbox.Envelope {
	if newULID == nil {
		newULID = ulid.New
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}

	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil
	}
	var rec Record
	// Returning this error would stall the reader on a line Anthropic wrote
	// mid-flush or in an unknown shape.
	if err := json.Unmarshal(trimmed, &rec); err != nil {
		return nil
	}

	var envs []*outbox.Envelope
	switch rec.Type {
	case "ai-title":
		envs = append(envs, deriveTitle(state, events.TranscriptAITitle, rec.AiTitle, now, newULID)...)
	case "custom-title":
		envs = append(envs, deriveTitle(state, events.TranscriptCustomTitle, rec.CustomTitle, now, newULID)...)
	}
	if env := deriveHeartbeat(state, rec.Timestamp, now); env != nil {
		envs = append(envs, env)
	}
	return envs
}

func deriveHeartbeat(state *State, rawTimestamp string, now Clock) *outbox.Envelope {
	if rawTimestamp == "" {
		return nil
	}
	ts, err := time.Parse(time.RFC3339Nano, rawTimestamp)
	if err != nil {
		return nil
	}
	minute := ts.UTC().Truncate(time.Minute)
	if !state.markMinute(minute) {
		return nil
	}
	return &outbox.Envelope{
		ULID:            ActivityMinuteID(state.ClaudeSessionID, minute),
		Type:            string(events.ActivityMinute),
		ClaudeSessionID: state.ClaudeSessionID,
		Payload:         map[string]any{},
		CreatedAt:       now(),
		TS:              &minute,
	}
}

func deriveTitle(state *State, eventType events.Type, title string, now Clock, newULID ULIDFn) []*outbox.Envelope {
	// A title line carries no id of its own, so the (title, kind) pair is its
	// only dedup key. Without it, a re-read after a truncation re-emits an
	// unchanged title stamped at re-read time, which advances the card's
	// lastEventAt and resurfaces shipped cards on the dashboard.
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
