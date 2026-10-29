package cli

import (
	"errors"
	"fmt"
	"net"

	"github.com/spf13/cobra"

	"github.com/valian-ca/homebrew-tools/internal/lanes/env"
	"github.com/valian-ca/homebrew-tools/internal/lanes/lane"
	"github.com/valian-ca/homebrew-tools/internal/lanes/paths"
	"github.com/valian-ca/homebrew-tools/internal/lanes/workspace"
)

const lanProbe = "1.1.1.1:53"

func newHostCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "host [lan|local]",
		Short: "Show or switch the host the lane's clients reach its stack by",
		Long: `Show or switch LANES_HOST, the address the lane's clients reach its stack by.
It is 127.0.0.1 by default. lanes env exports it, and generate templates see it.

  lan    the IP of the interface that carries the default route, read anew at
         each call, so a physical device on the same network reaches the
         lane's emulators; without a network, it refuses and the host stays
  local  back to 127.0.0.1

Switching regenerates the templates and never restarts the stack. Shells
already open keep the old value, which still works on the Mac. The host lasts
as long as the lane: it survives stops and restarts of the stack, and a new
lane starts again on 127.0.0.1. The emulators listen on every interface in
both modes, so the local network reaches them either way.`,
		Example: `  lanes host
  lanes host lan && eval "$(lanes env)"
  lanes host local`,
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: []string{"lan", "local"},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			dir, err := cwd()
			if err != nil {
				return err
			}
			w, err := workspace.Settle(ctx, dir)
			if err != nil {
				return err
			}
			current := w.Vars.Map()["LANES_HOST"]
			if len(args) == 0 {
				printf(cmd.OutOrStdout(), "%s\n", current)
				return nil
			}
			var host string
			switch args[0] {
			case "lan":
				ip, err := lanIP()
				if err != nil {
					return fmt.Errorf("lanes host lan: no network (no default route): the host stays %s", current)
				}
				host = ip
			case "local":
				host = ""
			default:
				return fmt.Errorf("lanes host: unknown mode %q — lan or local", args[0])
			}
			if err := lane.SetHost(w.WT, w.Lane.ID, host); err != nil {
				return err
			}
			w.SetHost(host)
			if err := prepare(ctx, w, cmd.ErrOrStderr()); err != nil {
				return err
			}
			printf(cmd.OutOrStdout(), "LANES_HOST=%s for lane %s — templates regenerated; run eval \"$(lanes env)\" in open shells\n", w.Vars.Map()["LANES_HOST"], w.Lane.ID)
			return nil
		},
	}
}

// lanIP reads the local address the kernel picks to reach the internet; a UDP
// dial sends no packet.
func lanIP() (string, error) {
	target := lanProbe
	if v, ok := paths.TestOverride("LANES_LAN_PROBE"); ok {
		if v == "unreachable" {
			return "", errors.New("unreachable")
		}
		target = v
	}
	conn, err := net.Dial("udp4", target)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || addr.IP.IsUnspecified() {
		return "", errors.New("no local address")
	}
	if addr.IP.String() == env.DefaultHost {
		return "", errors.New("only the loopback")
	}
	return addr.IP.String(), nil
}
