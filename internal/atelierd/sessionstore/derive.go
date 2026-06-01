package sessionstore

import (
	"time"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/events"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/outbox"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/ulid"
)

// Clock and ULIDFn are injection points so tests can drive derivation
// deterministically; production passes nil and gets time.Now() and ulid.NewAt.
// ULIDFn takes the timestamp to stamp into the ULID — the session's real
// activity time, not wall-clock — so a startup scan doesn't re-date old events.
type Clock func() time.Time
type ULIDFn func(time.Time) string

// Derive compares the freshly-scanned entry against the last-emitted state and
// returns the envelope to ship, or nil when nothing should be emitted. It does
// not mutate state — the caller persists the new (title, source) only after a
// successful derive, in the crash-safe save-before-write order.
//
// A nil state means the session has never emitted a title. First sight of a
// titled session emits (startup capture); first sight of a session that carries
// no title stays silent, so we never null a field that was never set.
func Derive(state *State, entry Entry, now Clock, newULID ULIDFn) ([]*outbox.Envelope, error) {
	if newULID == nil {
		newULID = ulid.NewAt
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}

	if state != nil && entry.Title == state.LastTitle && eventTypeFor(entry.TitleSource) == eventTypeFor(state.LastTitleSource) {
		return nil, nil
	}
	if state == nil && entry.Title == "" {
		return nil, nil
	}

	// Stamp the event with the session's real activity time, not wall-clock, so
	// a startup scan of idle sessions doesn't bump their downstream lastEventAt.
	stamp := entry.ActivityAt
	if stamp.IsZero() {
		stamp = now()
	}

	return []*outbox.Envelope{{
		ULID:            newULID(stamp),
		Type:            string(eventTypeFor(entry.TitleSource)),
		ClaudeSessionID: entry.CliSessionID,
		Payload:         map[string]any{"title": entry.Title},
		CreatedAt:       now(),
	}}, nil
}

// eventTypeFor maps the store's titleSource to an event type. "user" is the
// value Desktop writes after a manual rename; "auto", null, and any unknown
// value are treated as the auto-generated kind (the dominant case), confirmed
// empirically against the live store.
func eventTypeFor(titleSource string) events.Type {
	if titleSource == "user" {
		return events.TranscriptCustomTitle
	}
	return events.TranscriptAITitle
}
