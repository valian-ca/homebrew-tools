package cmds

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/events"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/outbox"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/ulid"
)

// NewEmitCmd builds the `atelierd emit <type> <claudeSessionId>` sub-command.
func NewEmitCmd() *cobra.Command {
	var dataPairs []string
	c := &cobra.Command{
		Use:   "emit <type> <claudeSessionId>",
		Short: "Append an event to the outbox for later shipping",
		Long: `Validate <type> against the atelier event taxonomy, build the JSON envelope,
and write it atomically to ~/.atelier/outbox/<ulid>.json.

The host, uid, and ts fields are intentionally absent — atelierd run adds them
at ship time. emit is auth-decoupled: it succeeds even when no Firebase link
is established yet (per AC 5).

Examples:
  atelierd emit hook:user-prompt-submit cs-abc123
  atelierd emit hook:pre-tool-use cs-abc123 --data tool=Edit
  atelierd emit skill:phase-start cs-abc123 --data phase=blueprint --data ticketId=VAL-164`,
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

			env := &outbox.Envelope{
				ULID:            ulid.New(),
				Type:            eventType,
				ClaudeSessionID: claudeSessionID,
				Payload:         payload,
				CreatedAt:       time.Now().UTC(),
			}
			return outbox.Write(env)
		},
	}
	c.Flags().StringArrayVar(&dataPairs, "data", nil, "key=value entry to add to payload (repeatable)")
	return c
}
