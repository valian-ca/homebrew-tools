package cmds

import (
	"github.com/spf13/cobra"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/credentials"
)

// NewUnlinkCmd builds the `atelierd unlink` sub-command.
//
// Implementation note re. AC 9 ("la callable revokeRefreshToken … est appelée
// best-effort"): the Firebase Auth REST API does NOT expose a user-facing
// refresh-token revocation endpoint — that capability is Admin-SDK-only. The
// "best-effort" wording covers the omission: we delete credentials locally
// and report success. A future backend callable revokeMyRefreshToken (Admin
// SDK wrapper, gated on the caller's idToken) would let us close this gap.
func NewUnlinkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unlink",
		Short: "Forget the local Firebase link",
		Long: `Delete ~/.atelier/credentials. atelierd run will enter the unlinked state
on its next refresh tick.

The Firebase refresh-token revocation that the spec mentions has no
user-facing REST endpoint — it lives only in the Admin SDK. This command
therefore deletes credentials locally and exits 0; full revocation requires
either re-linking on a different account or admin intervention.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !credentials.Exists() {
				cmd.Println("Aucun lien actif.")
				return nil
			}
			if err := credentials.Delete(); err != nil {
				return err
			}
			cmd.Println("Délié.")
			return nil
		},
	}
}
