package cmds

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/devicebank"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/forge"
)

const (
	ExitBankExhausted      = 10
	ExitBankNotInitialized = 11
	ExitForgeUnknownRun    = 20
	ExitForgeInvalidPass   = 21
	ExitForgeCampaign      = 22
	ExitForgeWaveCap       = 23
	ExitForgeStaging       = 24
	ExitForgeAmbiguousRun  = 25
)

func ExitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, devicebank.ErrExhausted):
		return ExitBankExhausted
	case errors.Is(err, devicebank.ErrNotInitialized):
		return ExitBankNotInitialized
	case errors.Is(err, forge.ErrUnknownRun):
		return ExitForgeUnknownRun
	case errors.Is(err, forge.ErrInvalidPass):
		return ExitForgeInvalidPass
	case errors.Is(err, forge.ErrCampaignInvalid):
		return ExitForgeCampaign
	case errors.Is(err, forge.ErrWaveCap):
		return ExitForgeWaveCap
	case errors.Is(err, forge.ErrInvalidStaging):
		return ExitForgeStaging
	case errors.Is(err, forge.ErrAmbiguousRun):
		return ExitForgeAmbiguousRun
	}
	return 1
}

func NewDeviceCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "device",
		Short: "Manage the mobile device bank: provisioning, leases, recycling",
		PersistentPostRun: func(cmd *cobra.Command, _ []string) {
			if f := cmd.Flags().Lookup("session"); f != nil {
				warnHolder(cmd.ErrOrStderr(), f.Value.String())
			}
		},
	}
	c.AddCommand(
		newBankCmd(),
		newLeaseCmd(),
		newPauseCmd(),
		newReleaseCmd(),
		newDeviceStatusCmd(),
		newRecycleCmd(),
	)
	return c
}

func warnHolder(w io.Writer, session string) {
	if msg := devicebank.HolderWarning(session); msg != "" {
		fmt.Fprintln(w, msg)
	}
}

func newBankCmd() *cobra.Command {
	bank := &cobra.Command{
		Use:   "bank",
		Short: "Provision the device bank and set its ignition limit",
	}
	var nIOS, nAndroid int
	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Provision the bank to N iOS simulator clones and N AVDs",
		Long: `Provision the device bank: N iOS simulator clones named atelier-ios-1..N
and N AVDs named atelier-android-1..N (default 2 + 2).

Idempotent and two-way sizing: re-running tops the bank up; shrinking deletes
the idle excess clones, while a leased or recycling clone is kept (warned,
removed on a later pass). Every unleased AVD in range is recreated on the
current device profile; at most 16 AVDs (adb discovers console ports
5554-5584 only). A broken device is repaired and leasable again. A machine
missing one toolchain provisions the other side, warns, and exits 0. The
ignition limit (bank limit) is kept.`,
		Example: `  atelierd device bank init
  atelierd device bank init --ios 1 --android 2`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return devicebank.InitBank(cmd.Context(), nIOS, nAndroid, cmd.ErrOrStderr())
		},
	}
	initCmd.Flags().IntVar(&nIOS, "ios", 2, "number of iOS simulator clones")
	initCmd.Flags().IntVar(&nAndroid, "android", 2, "number of Android AVDs")
	limitCmd := &cobra.Command{
		Use:   "limit [N]",
		Short: "Print or set how many virtual devices may be powered on at once",
		Long: fmt.Sprintf(`Print the ignition limit, or set it to N (at least 1): the number of
virtual bank devices powered on at the same time, all sessions and
platforms together. Default %d. A suspended lease does not count; a
recycling device counts until its power-off is confirmed.`, devicebank.DefaultMaxOn),
		Example: `  atelierd device bank limit
  atelierd device bank limit 2`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				n, err := devicebank.MaxOn()
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), n)
				return nil
			}
			n, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid limit %q: %w", args[0], err)
			}
			return devicebank.SetMaxOn(n)
		},
	}
	bank.AddCommand(initCmd, limitCmd)
	return bank
}

func newLeaseCmd() *cobra.Command {
	var platformFlag, session string
	var wait time.Duration
	c := &cobra.Command{
		Use:   "lease --platform ios|android --session <id> [--wait <duration>]",
		Short: "Lease a device of the platform for the session",
		Long: `Select a device of the platform, power it on, record the lease, and print
the targetable identifier (simulator UDID or Android serial) alone on
stdout. A device that fails to boot goes to recycling and keeps counting
toward the ignition limit until its power-off is confirmed: another device
is tried as soon as the limit allows, within --wait. A connected physical
device is leased only when the platform has no virtual device left to
power on.

Idempotent per (session, platform): a session that already holds a lease
gets the same device back. A suspended lease (see pause), or one whose
device died, gets its own device powered back on, data kept.

Powering a virtual device on obeys the ignition limit (bank limit) and a
first-come queue: when no device can be served, the session waits its turn
up to --wait (default 0: queue once and exit), then exits 10. The queue
keeps its place across calls and forgets it 2 min after the last call's
deadline, or at once when the wait is interrupted (Ctrl-C, SIGTERM). After
10 min of waiting, one macOS notification is posted.

Exit codes: 0 leased, 10 not served within the wait (bank exhausted or
ignition limit reached), 11 bank not initialized.`,
		Example: `  atelierd device lease --platform android --session abc123
  atelierd device lease --platform ios --session abc123 --wait 15m`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			platform, err := devicebank.ParsePlatform(platformFlag)
			if err != nil {
				return err
			}
			workdir, _ := os.Getwd()
			id, err := devicebank.Acquire(cmd.Context(), session, workdir, platform, wait, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), id)
			return nil
		},
	}
	c.Flags().StringVar(&platformFlag, "platform", "", "ios or android (required)")
	c.Flags().StringVar(&session, "session", "", "Claude session ID holding the lease (required)")
	c.Flags().DurationVar(&wait, "wait", 0, "how long to wait in the queue before exiting 10")
	_ = c.MarkFlagRequired("platform")
	_ = c.MarkFlagRequired("session")
	return c
}

func newPauseCmd() *cobra.Command {
	var session string
	c := &cobra.Command{
		Use:   "pause --session <id>",
		Short: "Suspend the session's leases: power its virtual devices off, keep their data",
		Long: `Suspend every lease of the session. A virtual device is powered off with
its data and installed app kept, and stops counting toward the ignition
limit; the session keeps the lease. The session's next lease powers the
same device back on, through the queue when the limit is reached.

A session without a lease is a no-op. Idempotent. A physical device is
left as it is.`,
		Example: `  atelierd device pause --session abc123`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return devicebank.Pause(cmd.Context(), session)
		},
	}
	c.Flags().StringVar(&session, "session", "", "Claude session ID to suspend (required)")
	_ = c.MarkFlagRequired("session")
	return c
}

func newReleaseCmd() *cobra.Command {
	var platformFlag, session string
	c := &cobra.Command{
		Use:   "release --session <id> [--platform ios|android]",
		Short: "Release the session's leases (all platforms by default)",
		Long: `Release the session's leases and queue entries, and return immediately: a
virtual device is powered off and erased, without being booted, by a
detached worker, and goes back to off within 5 min (an AVD is wiped by
-wipe-data on its next boot; a recycle that fails twice leaves the device
broken until bank init); a physical device is leasable again on the spot,
no erase, no power-off.`,
		Example: `  atelierd device release --session abc123
  atelierd device release --session abc123 --platform android`,
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
		Short: "List every bank device with its state and lease, and the queue",
		Long: `List every bank device and connected physical device. STATE is off, on,
recycling or broken; LEASE starts with the holding session's id, and a
broken device shows its reason there. The recorded state is checked
against the simulators and emulators actually running first. When
sessions are waiting, a QUEUE block lists them in order.`,
		Example: `  atelierd device status
  atelierd device status | grep abc123`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if devicebank.Exists() {
				if err := devicebank.Reap(cmd.Context()); err != nil {
					return err
				}
			}
			rows, queue, err := devicebank.StatusRows(cmd.Context())
			if err != nil {
				return err
			}
			if len(rows) == 0 {
				fmt.Fprintln(cmd.ErrOrStderr(), "device bank is empty — run `atelierd device bank init`")
				return nil
			}
			return renderStatus(cmd.OutOrStdout(), rows, queue, time.Now())
		},
	}
}

func renderStatus(out io.Writer, rows []devicebank.StatusRow, queue []*devicebank.Waiter, now time.Time) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tPLATFORM\tTYPE\tSTATE\tLEASE")
	for _, r := range rows {
		kind := "virtual"
		if r.Physical {
			kind = "physical"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Name, r.Platform, kind, r.State, leaseText(r, now))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if len(queue) == 0 {
		return nil
	}
	fmt.Fprintln(out, "\nQUEUE")
	for i, q := range queue {
		fmt.Fprintf(out, "%d. session %s (%s) waiting since %s\n", i+1, q.SessionID, q.Platform, clockTime(q.EnqueuedAt))
	}
	return nil
}

func leaseText(r devicebank.StatusRow, now time.Time) string {
	if r.Device != nil && r.Device.State == devicebank.StateBroken {
		return "broken: " + r.Device.BrokenReason
	}
	l := r.Lease
	if l == nil {
		return "-"
	}
	renewed := now.Sub(l.RenewedAt).Round(time.Second)
	switch {
	case r.Physical:
		return fmt.Sprintf("session %s, age %s, renewed %s ago", l.SessionID, now.Sub(l.AcquiredAt).Round(time.Second), renewed)
	case l.Suspended():
		return fmt.Sprintf("session %s, suspended since %s", l.SessionID, clockTime(l.SuspendedAt))
	case l.DeviceDied:
		return fmt.Sprintf("session %s, device died — restarts at next lease", l.SessionID)
	}
	return fmt.Sprintf("session %s, on since %s, renewed %s ago", l.SessionID, clockTime(r.Device.PoweredOnAt), renewed)
}

func clockTime(t time.Time) string {
	return t.Local().Format("15:04")
}

func newRecycleCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "recycle <name>",
		Short:  "Recycle worker (spawned detached when a lease ends or a device fails; not for direct use)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return devicebank.RunRecycle(cmd.Context(), args[0])
		},
	}
}
