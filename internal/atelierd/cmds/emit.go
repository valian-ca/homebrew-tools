package cmds

import (
	"errors"
	"fmt"
	"os"
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

Examples:
  atelierd emit hook:user-prompt-submit cs-abc123
  atelierd emit hook:pre-tool-use cs-abc123 --data tool=Edit
  atelierd emit skill:phase-start cs-abc123 --data phase=blueprint --data ticketId=VAL-164
  atelierd emit hook:assistant-turn cs-abc123 --data model=gpt-5.6-sol --data-json usage='{"input_tokens":1200,"output_tokens":90}'`,
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

			// On hook:session-start with --data jsonlPath, register the
			// session BEFORE writing the outbox envelope so a daemon that
			// (re)starts immediately afterward sees the session record on
			// its first scan of ~/.atelier/sessions/. The state file is the
			// only durable handoff between the bash hook (which knows the
			// transcript path) and the long-lived watcher inside atelierd run.
			if eventType == string(events.HookSessionStart) {
				if raw, present := payload["jsonlPath"]; present {
					// A malformed value (a --data-json number, object, null…)
					// used to skip registration silently and exit 0 — the
					// session then never got a watcher and nobody knew why.
					jsonlPath, ok := raw.(string)
					if !ok || jsonlPath == "" {
						return fmt.Errorf("jsonlPath must be a non-empty string, got %T (%v)", raw, raw)
					}
					if err := transcript.SaveState(&transcript.State{
						ClaudeSessionID: claudeSessionID,
						JSONLPath:       jsonlPath,
						LastActivityAt:  time.Now().UTC(),
					}); err != nil {
						return fmt.Errorf("register session: %w", err)
					}
				} else if _, err := transcript.LoadState(claudeSessionID); errors.Is(err, os.ErrNotExist) {
					// No jsonlPath at all is the OpenCode signature — there is
					// no transcript to watch, but the state record makes the
					// session visible to the session-end janitor, which
					// synthesizes its close after the idle timeout. Never
					// overwrite an existing state: a rebirth session-start
					// must not reset a real watcher's offset.
					if serr := transcript.SaveState(&transcript.State{
						ClaudeSessionID: claudeSessionID,
						LastActivityAt:  time.Now().UTC(),
					}); serr != nil {
						return fmt.Errorf("register session: %w", serr)
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
			// releases them (VAL-268). Best-effort by design — a lease-state
			// failure never fails the emit.
			devicebank.OnEmit(claudeSessionID, eventType == string(events.HookSessionEnd))
			touchJSONLLessSession(claudeSessionID, eventType == string(events.HookSessionEnd))
			return nil
		},
	}
	c.Flags().StringArrayVar(&dataPairs, "data", nil, "key=value entry to add to payload (repeatable)")
	c.Flags().StringArrayVar(&dataJSONPairs, "data-json", nil, "key=<json> entry to add to payload, value parsed as JSON (repeatable)")
	return c
}

// touchJSONLLessSession anchors the session-end janitor's idle clock at the
// last emitted event for transcript-less sessions (OpenCode signature):
// nothing else refreshes their LastActivityAt — consume() only touches
// watcher-backed states. A session-end retires the state instead, so the
// janitor never synthesizes a second close after a clean quit. Best-effort
// like devicebank.OnEmit — a state failure never fails the emit.
func touchJSONLLessSession(claudeSessionID string, sessionEnd bool) {
	s, err := transcript.LoadState(claudeSessionID)
	if err != nil || s.JSONLPath != "" {
		return
	}
	if sessionEnd {
		_ = transcript.DeleteState(s.Key())
		return
	}
	s.LastActivityAt = time.Now().UTC()
	_ = transcript.SaveState(s)
}
