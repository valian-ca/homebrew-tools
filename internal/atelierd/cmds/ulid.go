package cmds

import (
	"github.com/spf13/cobra"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/ulid"
)

// NewULIDCmd builds the `atelierd ulid` sub-command.
func NewULIDCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ulid",
		Short: "Print a fresh ULID to stdout",
		Long: `Generate a Crockford-base32 26-char ULID with monotonic entropy and print it.

Used by Claude Code hooks and by ` + "`atelierd emit`" + ` itself. Sub-millisecond.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.Println(ulid.New())
			return nil
		},
	}
}
