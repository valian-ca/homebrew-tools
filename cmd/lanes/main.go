package main

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/valian-ca/homebrew-tools/internal/lanes/cli"
)

var version = "dev"

//go:embed README.md
var contract string

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	root := cli.NewRoot(version, contract)
	root.SetOut(os.Stdout)
	root.SetErr(os.Stderr)
	if err := root.ExecuteContext(ctx); err != nil {
		if !cli.Quiet(err) {
			fmt.Fprintln(os.Stderr, "lanes: "+err.Error())
		}
		os.Exit(cli.ExitCode(err))
	}
}
