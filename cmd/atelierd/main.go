// Command atelierd is the Atelier dashboard's local daemon — the bridge
// between Claude Code hooks/skills running on an associate's laptop and the
// Firestore /events stream consumed by the orchestration dashboard.
//
// See VAL-164 (and parent VAL-162) for the full specification. Distributed
// via the valian-ca/homebrew-tools tap; brew install compiles from source via
// `go => :build`.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/cmds"
)

// version is stamped at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	// Propagate the build version into the run loop so it can write it into
	// the status file without an import cycle.
	cmds.Version = version

	root := &cobra.Command{
		Use:     "atelierd",
		Short:   "Atelier dashboard daemon",
		Long:    "atelierd bridges Claude Code activity on this machine to the Atelier dashboard cloud layer.",
		Version: version,
		// Cobra otherwise prints the error itself, on top of our main()
		// fallback below — silence both errors and usage so we control
		// the output exactly once.
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.AddCommand(
		cmds.NewULIDCmd(),
		cmds.NewEmitCmd(),
		cmds.NewLinkCmd(),
		cmds.NewUnlinkCmd(),
		cmds.NewStatusCmd(),
		cmds.NewRunCmd(),
	)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := root.ExecuteContext(ctx); err != nil {
		// `atelierd status` returns a sentinel error to signal at-least-one-FAIL
		// without dumping a usage block — exit 1 cleanly in that case.
		if cmds.IsStatusFail(err) {
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "atelierd: "+err.Error())
		os.Exit(1)
	}
}
