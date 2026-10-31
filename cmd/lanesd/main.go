package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/valian-ca/homebrew-tools/internal/lanes/daemon"
)

var version = "dev"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := newRoot().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "lanesd: "+err.Error())
		if errors.Is(err, daemon.ErrAlreadyRunning) {
			os.Exit(0)
		}
		os.Exit(1)
	}
}

func newRoot() *cobra.Command {
	return &cobra.Command{
		Use:   "lanesd",
		Short: "The lanes daemon: runs, watches and stops worktree stacks",
		Long: `lanesd runs the stacks that lanes start asks for, watches for Claude sessions
in each worktree, stops a stack when its sessions are gone, and clears what
remains of worktrees that disappeared. One per Mac, run as a Homebrew service:

  brew services start lanes

On startup it takes back the stacks still running and clears leftovers of
deleted worktrees; it never relaunches a stack.

With a mysql resource, reconciliation erases the databases of a gone lane —
from the main checkout, with its lanes.yaml credentials — and retries every
5 minutes when the server sleeps, does not answer or its port guard refuses.
lanesd never wakes nor stops the MySQL server.

Its log goes to stderr, which the Homebrew service writes to
$(brew --prefix)/var/log/lanes.log.`,
		Example: `  brew services start lanes
  lanesd    # in the foreground, for debugging`,
		Version:       version,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return daemon.New().Run(cmd.Context())
		},
	}
}
