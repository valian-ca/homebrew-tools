package cmds

import (
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/devicebank"
)

// Distinct exit codes for scriptable lease failures (VAL-268).
const (
	ExitBankExhausted      = 10
	ExitBankNotInitialized = 11
)

// ExitCode maps an error returned by a device sub-command to the process
// exit code main() should use.
func ExitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, devicebank.ErrExhausted):
		return ExitBankExhausted
	case errors.Is(err, devicebank.ErrNotInitialized):
		return ExitBankNotInitialized
	}
	return 1
}

// NewDeviceCmd builds the `atelierd device` command group: the machine-local
// source of truth for mobile-device attribution (bank, leases, recycling).
func NewDeviceCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "device",
		Short: "Manage the mobile device bank: provisioning, leases, recycling",
	}
	c.AddCommand(
		newBankCmd(),
		newLeaseCmd(),
		newReleaseCmd(),
		newDeviceStatusCmd(),
		newRecycleCmd(),
	)
	return c
}

func newBankCmd() *cobra.Command {
	bank := &cobra.Command{
		Use:   "bank",
		Short: "Provision the device bank",
	}
	var nIOS, nAndroid int
	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Provision the bank to N iOS simulator clones and N AVDs",
		Long: `Provision the device bank: N iOS simulator clones named atelier-ios-1..N
and N AVDs named atelier-android-1..N (default 2 + 2).

Idempotent and two-way sizing: re-running tops the bank up; shrinking deletes
the free excess clones, while a leased clone is never touched (warned,
removed on a later pass). A machine missing one toolchain provisions the
other side, warns, and exits 0.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return devicebank.InitBank(cmd.Context(), nIOS, nAndroid, cmd.ErrOrStderr())
		},
	}
	initCmd.Flags().IntVar(&nIOS, "ios", 2, "number of iOS simulator clones")
	initCmd.Flags().IntVar(&nAndroid, "android", 2, "number of Android AVDs")
	bank.AddCommand(initCmd)
	return bank
}

func newLeaseCmd() *cobra.Command {
	var platformFlag, session string
	c := &cobra.Command{
		Use:   "lease --platform ios|android --session <id>",
		Short: "Lease a free device of the platform for the session",
		Long: `Non-blocking. Select a free device of the platform: health-check it (a
wedged device goes to recycling and the next one is tried), boot it when
cold, record the lease, and print the targetable identifier (simulator UDID
or Android serial) alone on stdout.

Idempotent per (session, platform): a session that already holds a lease
gets the same device back. Exit codes: 0 leased, 10 bank exhausted or
recycling-only, 11 bank not initialized.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			platform, err := devicebank.ParsePlatform(platformFlag)
			if err != nil {
				return err
			}
			workdir, _ := os.Getwd()
			id, err := devicebank.Acquire(cmd.Context(), session, workdir, platform, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), id)
			return nil
		},
	}
	c.Flags().StringVar(&platformFlag, "platform", "", "ios or android (required)")
	c.Flags().StringVar(&session, "session", "", "Claude session ID holding the lease (required)")
	_ = c.MarkFlagRequired("platform")
	_ = c.MarkFlagRequired("session")
	return c
}

func newReleaseCmd() *cobra.Command {
	var platformFlag, session string
	c := &cobra.Command{
		Use:   "release --session <id> [--platform ios|android]",
		Short: "Release the session's leases (all platforms by default)",
		Long: `Release the session's leases and return immediately: a virtual device
erases and reboots (recycling) in a detached background worker; a physical
device is leasable again on the spot, no erase, no reboot.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var platform devicebank.Platform
			if platformFlag != "" {
				p, err := devicebank.ParsePlatform(platformFlag)
				if err != nil {
					return err
				}
				platform = p
			}
			return devicebank.Release(cmd.Context(), session, platform)
		},
	}
	c.Flags().StringVar(&platformFlag, "platform", "", "limit the release to one platform")
	c.Flags().StringVar(&session, "session", "", "Claude session ID to release (required)")
	_ = c.MarkFlagRequired("session")
	return c
}

func newDeviceStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "List every bank device with its state and lease",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Reap first so the listing reflects expired leases instead of
			// showing devices as held by long-dead sessions.
			if devicebank.Exists() {
				if err := devicebank.Reap(cmd.Context()); err != nil {
					return err
				}
			}
			rows, err := devicebank.StatusRows(cmd.Context())
			if err != nil {
				return err
			}
			if len(rows) == 0 {
				fmt.Fprintln(cmd.ErrOrStderr(), "device bank is empty — run `atelierd device bank init`")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tPLATFORM\tTYPE\tSTATE\tLEASE")
			now := time.Now()
			for _, r := range rows {
				kind := "virtual"
				if r.Physical {
					kind = "physical"
				}
				lease := "-"
				if r.Lease != nil {
					lease = fmt.Sprintf("session %s, age %s, renewed %s ago",
						r.Lease.SessionID,
						now.Sub(r.Lease.AcquiredAt).Round(time.Second),
						now.Sub(r.Lease.RenewedAt).Round(time.Second))
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Name, r.Platform, kind, r.State, lease)
			}
			return w.Flush()
		},
	}
}

func newRecycleCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "recycle <name>",
		Short:  "Recycle worker (spawned detached by release; not for direct use)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return devicebank.RunRecycle(cmd.Context(), args[0])
		},
	}
}
