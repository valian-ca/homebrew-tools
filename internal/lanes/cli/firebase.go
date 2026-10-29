package cli

import (
	"errors"
	"os"

	"github.com/spf13/cobra"

	"github.com/valian-ca/homebrew-tools/internal/lanes/firebase"
	"github.com/valian-ca/homebrew-tools/internal/lanes/procinfo"
	"github.com/valian-ca/homebrew-tools/internal/lanes/workspace"
)

func newFirebaseCmd() *cobra.Command {
	c := &cobra.Command{
		Use:    "firebase",
		Short:  "Internal: the firebase piece of the stack",
		Hidden: true,
	}
	c.AddCommand(&cobra.Command{
		Use:   "run",
		Short: "Run the firebase emulators as the stack's firebase piece; process-compose starts it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := cwd()
			if err != nil {
				return err
			}
			w, err := workspace.Inspect(cmd.Context(), dir)
			if err != nil {
				return err
			}
			fb := w.Cfg.Firebase()
			if fb == nil {
				return errors.New("lanes.yaml declares no firebase resource")
			}
			return firebase.Run(cmd.Context(), firebase.RunSpec{
				Root:      w.WT.Root,
				Tmpdir:    w.Tmpdir,
				Firebase:  fb,
				PortNames: w.Cfg.Ports.Names,
				Block:     w.Block.Start,
				Environ:   os.Environ(),
				Out:       cmd.OutOrStdout(),
				Procs:     procinfo.System(),
			})
		},
	})
	return c
}
