package cmds

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/devicebank"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/events"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/outbox"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/transcript"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/ulid"
)

func NewEmitCmd() *cobra.Command {
	var dataPairs []string
	var dataJSONPairs []string
	c := &cobra.Command{
		Use:   "emit <type> <claudeSessionId>",
		Short: "Append an event to the outbox for later shipping",
		Long: `Validate <type> against the atelier event taxonomy, build the JSON envelope,
and write it atomically to ~/.atelier/outbox/<ulid>.json.

The host, uid, and ts fields are intentionally absent — atelierd run adds them
at ship time. emit is auth-decoupled: it succeeds even when no Firebase link
is established yet.

--data values are kept as verbatim strings; --data-json values are parsed as
JSON, for payload fields that need nesting or numeric types. When both flags
set the same key, the --data-json value wins.

activity:minute heartbeats are derived from the transcripts by atelierd run
and cannot be emitted by hand.

Examples:
  atelierd emit hook:session-start cs-abc123 --data jsonlPath=/path/to/cs-abc123.jsonl --data cwd=/path/to/repo
  atelierd emit skill:phase-start cs-abc123 --data phase=blueprint --data ticketId=VAL-164
  atelierd emit skill:phase-end cs-abc123 --data phase=blueprint --data outcome=completed`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			eventType := args[0]
			claudeSessionID := args[1]

			if !events.IsValid(eventType) {
				return fmt.Errorf("unknown event type %q (not in atelier taxonomy)", eventType)
			}
			if claudeSessionID == "" {
				return fmt.Errorf("claudeSessionId must be non-empty")
			}

			payload, err := events.ParsePayload(dataPairs)
			if err != nil {
				return err
			}
			if payload == nil {
				payload = map[string]any{}
			}
			jsonPayload, err := events.ParseJSONPayload(dataJSONPairs)
			if err != nil {
				return err
			}
			for key, val := range jsonPayload {
				payload[key] = val
			}

			// Register the session BEFORE writing the outbox envelope so a
			// daemon that (re)starts immediately afterward sees it on its first
			// scan of ~/.atelier/sessions/. The state file is the only durable
			// handoff between the plugin hook (which knows the transcript path)
			// and the session reader inside atelierd run.
			if eventType == string(events.HookSessionStart) {
				if raw, present := payload["jsonlPath"]; present {
					jsonlPath, ok := raw.(string)
					if !ok || jsonlPath == "" {
						return fmt.Errorf("jsonlPath must be a non-empty string, got %T (%v)", raw, raw)
					}
					if err := registerSession(claudeSessionID, jsonlPath); err != nil {
						return fmt.Errorf("register session: %w", err)
					}
				}
			}

			env := &outbox.Envelope{
				ULID:            ulid.New(),
				Type:            eventType,
				ClaudeSessionID: claudeSessionID,
				Payload:         payload,
				CreatedAt:       time.Now().UTC(),
			}
			if err := outbox.Write(env); err != nil {
				return err
			}
			// Every emit renews the session's device leases; session-end
			// releases them. Best-effort by design — a lease-state
			// failure never fails the emit.
			devicebank.OnEmit(claudeSessionID, eventType == string(events.HookSessionEnd))
			return nil
		},
	}
	c.Flags().StringArrayVar(&dataPairs, "data", nil, "key=value entry to add to payload (repeatable)")
	c.Flags().StringArrayVar(&dataJSONPairs, "data-json", nil, "key=<json> entry to add to payload, value parsed as JSON (repeatable)")
	return c
}

// registerSession records the transcript of a session-start. Resume and
// /compact fire session-start again on the same transcript: its read state is
// kept, so nothing already read is re-read. A different transcript resets the
// offsets, which belong to the old file, but keeps the emitted minutes, which
// belong to the session. An unreadable state is replaced rather than letting
// it block registration forever.
func registerSession(claudeSessionID, jsonlPath string) error {
	now := time.Now().UTC()
	return transcript.UpdateState(claudeSessionID, func(state *transcript.State, err error) *transcript.State {
		switch {
		case err == nil && state.JSONLPath == jsonlPath:
			state.LastActivityAt = now
		case err == nil:
			state.JSONLPath = jsonlPath
			state.Offsets = nil
			state.LastActivityAt = now
		default:
			state = &transcript.State{
				ClaudeSessionID: claudeSessionID,
				JSONLPath:       jsonlPath,
				LastActivityAt:  now,
			}
		}
		return state
	})
}
