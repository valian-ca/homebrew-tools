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

var version = "dev"

func main() {
	cmds.Version = version

	root := &cobra.Command{
		Use:           "atelierd",
		Short:         "Atelier dashboard daemon",
		Long:          "atelierd bridges Claude Code activity on this machine to the Atelier dashboard cloud layer.",
		Version:       version,
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
		cmds.NewWorktreeModeCmd(),
		cmds.NewDeviceCmd(),
		cmds.NewForgeCmd(),
	)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := root.ExecuteContext(ctx); err != nil {
		if cmds.IsStatusFail(err) {
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "atelierd: "+err.Error())
		os.Exit(cmds.ExitCode(err))
	}
}
