package cmds

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
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
		newPrepareCmd(),
		newReleaseCmd(),
		newDeviceStatusCmd(),
		newRecycleCmd(),
		newRestoreCmd(),
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
	var platformFlag, session, typeFlag, appID string
	var wait time.Duration
	var asJSON bool
	c := &cobra.Command{
		Use:   "lease --platform ios|android --session <id> [--type any|virtual|physical] [--app-id <id>] [--wait <duration>] [--json]",
		Short: "Lease a device of the platform for the session",
		Long: `Select a device of the platform, record the lease, and print the
targetable identifier (simulator UDID, Android serial, or physical device
id for flutter run -d) alone on stdout. With --json, stdout is instead one
JSON object on one line:
  {"id":"…","name":"…","serial":"…","platform":"ios","physical":true,
   "appId":"…","freshInstall":true}
where id is the targetable identifier and serial the one status shows.

--type says which devices the lease admits: any (default: physical or
virtual), virtual, or physical. Within the platform the lease returns, in
order:
  1. a physical device plugged by cable and free (any, physical);
  2. a virtual device through the queue (any, virtual).
Without --app-id, no new physical device is leased, whatever --type says.

A physical device is available when it is plugged by USB with debugging
allowed on this machine. One reachable only without cable (adb wifi, Wi-Fi
pairing to Xcode) is skipped. Leasing a physical device needs --app-id, the
run's application id: when that app is already installed and is neither a
developer build nor named "[D] …" (a Store build, say), the device is
unavailable for this lease and nothing on it is touched. A physical device
is never erased, powered off or recycled: on Android the lease sets
stay_on_while_plugged_in to 3, and the return puts back the value from
before the lease and removes the adb reverses. No app is ever uninstalled:
the build of --app-id stays installed after the return, so the next run
installs over it and keeps its permissions (iOS asks again for Local
Network, location and notifications after an install from scratch). A
skipped device is named on stderr, except while a suspended virtual lease
is checked for a physical device to give way to: only a device refused
for its app or its reads is named then.

Fresh install: when --app-id was not installed on the physical device as
the lease began, freshInstall is true and stderr carries a line starting
with "fresh install: ". The app's first launch then raises the system
permission alerts, among them the iOS Local Network alert that cuts it off
from emulators reached over the LAN until accepted. The signal belongs to
the lease: every call of the same lease repeats it, pauses included, and
a new lease (after a release) finds the app kept and does not.

A virtual device that fails to boot goes to recycling and keeps counting
toward the ignition limit until its power-off is confirmed: another device
is tried as soon as the limit allows, within --wait.

Idempotent per (session, platform): a session that already holds a lease
gets the same device back, except as the next paragraph says. A suspended
lease (see pause), or one whose device died, gets its own device back,
data kept; a physical lease is first checked to be still on the cable. A
physical lease whose device is no longer on the cable (checked twice, 2 s
apart) is dropped and the choice is made again.

A held lease whose kind --type no longer admits ends as release ends it,
and the choice is made again: a physical lease ends under --type virtual,
a virtual one under --type physical. A suspended virtual lease gives way
to a free physical device when --type admits one and --app-id is given:
the virtual device is recycled and the physical one leased. A powered-on
virtual lease is kept.

Powering a virtual device on obeys the ignition limit (bank limit) and a
first-come queue: when no device can be served, the session waits its turn
up to --wait (default 0: queue once and exit), then exits 10. The queue
keeps its place across calls and forgets it 2 min after the last call's
deadline, or at once when the wait is interrupted (Ctrl-C, SIGTERM). After
10 min of waiting, one macOS notification is posted. Under --type physical
the session never queues: it exits 10 when no physical device is plugged,
free and available within --wait.

Exit codes: 0 leased, 10 not served within the wait, 11 bank not
initialized. On any exit but 0, --json prints nothing on stdout.`,
		Example: `  atelierd device lease --platform android --session abc123 --app-id com.example.app.development
  atelierd device lease --platform ios --session abc123 --app-id com.example.app.development --json
  atelierd device lease --platform ios --session abc123 --type virtual --wait 15m`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			platform, err := devicebank.ParsePlatform(platformFlag)
			if err != nil {
				return err
			}
			req, err := devicebank.ParseRequirement(typeFlag)
			if err != nil {
				return err
			}
			workdir, _ := os.Getwd()
			l, err := devicebank.AcquireLease(cmd.Context(), devicebank.LeaseRequest{
				Session: session, Workdir: workdir, Platform: platform, Type: req, AppID: appID, Wait: wait,
			}, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			return printLeased(cmd.OutOrStdout(), l, asJSON)
		},
	}
	c.Flags().StringVar(&platformFlag, "platform", "", "ios or android (required)")
	c.Flags().StringVar(&session, "session", "", "Claude session ID holding the lease (required)")
	c.Flags().StringVar(&typeFlag, "type", string(devicebank.RequireAny), "devices the lease admits: any, virtual or physical")
	c.Flags().StringVar(&appID, "app-id", "", "application id of the run (required to lease a physical device)")
	c.Flags().DurationVar(&wait, "wait", 0, "how long to wait in the queue before exiting 10")
	c.Flags().BoolVar(&asJSON, "json", false, "print the lease as one JSON object, freshInstall included")
	_ = c.MarkFlagRequired("platform")
	_ = c.MarkFlagRequired("session")
	return c
}

func printLeased(out io.Writer, l devicebank.Leased, asJSON bool) error {
	if !asJSON {
		_, err := fmt.Fprintln(out, l.ID)
		return err
	}
	return json.NewEncoder(out).Encode(l)
}

func newPauseCmd() *cobra.Command {
	var platformFlag, session string
	c := &cobra.Command{
		Use:   "pause --session <id> [--platform ios|android]",
		Short: "Suspend the session's leases: power its virtual devices off, keep their data",
		Long: `Suspend every lease of the session, or only the one of --platform. A
virtual device is powered off with its data and installed app kept, and
stops counting toward the ignition limit; the session keeps the lease. The
session's next lease powers the same device back on, through the queue
when the limit is reached, unless a free physical device takes its place
(see lease).

A physical Android device gets back its stay_on_while_plugged_in value from
before the lease, and the next lease sets it to 3 again. A physical iOS
device is left as it is. Apps, adb reverses and power are never touched.

A session without a lease is a no-op. Idempotent.`,
		Example: `  atelierd device pause --session abc123
  atelierd device pause --session abc123 --platform ios`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			platform, err := optionalPlatform(platformFlag)
			if err != nil {
				return err
			}
			return devicebank.Pause(cmd.Context(), session, platform)
		},
	}
	c.Flags().StringVar(&platformFlag, "platform", "", "limit the pause to one platform")
	c.Flags().StringVar(&session, "session", "", "Claude session ID to suspend (required)")
	_ = c.MarkFlagRequired("session")
	return c
}

func optionalPlatform(flag string) (devicebank.Platform, error) {
	if flag == "" {
		return "", nil
	}
	return devicebank.ParsePlatform(flag)
}

func newPrepareCmd() *cobra.Command {
	var platformFlag, session string
	var reverses, grants []string
	var clearData bool
	c := &cobra.Command{
		Use:   "prepare --session <id> --platform android [--reverse tcp:<port>]... [--clear] [--grant <permission>]...",
		Short: "Prepare the session's leased physical Android device for its run",
		Long: `Act on the physical Android device the session leases, and on the
application id of its lease only (lease --app-id): nothing else on the
device, and no other device, can be reached through this command.

  --reverse tcp:<port>  adb reverse of that port to the same port on this
                        Mac, so the app reaches local services; the
                        lease's return removes every reverse.
  --clear               pm clear of the lease's app id: wipes its data
                        and permissions, never another app's.
  --grant <permission>  pm grant of that runtime permission to the lease's
                        app id (after --clear when both are given).

The lease must be held, not suspended, and on a physical Android device;
otherwise nothing is done and the command fails.`,
		Example: `  atelierd device prepare --session abc123 --platform android --reverse tcp:8080 --reverse tcp:9099
  atelierd device prepare --session abc123 --platform android --clear --grant android.permission.POST_NOTIFICATIONS`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			platform, err := devicebank.ParsePlatform(platformFlag)
			if err != nil {
				return err
			}
			return devicebank.Prepare(cmd.Context(), session, platform, devicebank.Preparation{
				Reverses: reverses, Clear: clearData, Grants: grants,
			})
		},
	}
	c.Flags().StringVar(&platformFlag, "platform", "", "android (required)")
	c.Flags().StringVar(&session, "session", "", "Claude session ID holding the lease (required)")
	c.Flags().StringArrayVar(&reverses, "reverse", nil, "tcp:<port> to reverse to the same port on this Mac (repeatable)")
	c.Flags().BoolVar(&clearData, "clear", false, "pm clear the lease's app id")
	c.Flags().StringArrayVar(&grants, "grant", nil, "runtime permission to grant the lease's app id (repeatable)")
	_ = c.MarkFlagRequired("platform")
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
broken until bank init). A physical device is never erased nor powered off:
a detached worker puts back its pre-lease state on Android
(stay_on_while_plugged_in, adb reverses), and the device is leasable again
once that restore succeeds. No app is uninstalled, the run's --app-id build
included: it stays installed for the next run. An iPhone has nothing to put
back and is leasable again at once. An unreachable Android device keeps its
restore pending until it is connected again; see restore --forget for one
that refuses it.`,
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
		Long: `List every bank device and physical device, one row per device. STATE is
off, on, recycling or broken. ID is the targetable identifier, followed by
the serial when it differs. LEASE starts with the holding session's id; a
broken device shows its reason there, a physical device reachable without
cable only shows "not leasable: wireless only", and a physical device still
owed its pre-lease state shows its pending restore. The recorded state is
checked against the simulators and emulators actually running first. When
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
	fmt.Fprintln(w, "NAME\tPLATFORM\tTYPE\tSTATE\tID\tLEASE")
	for _, r := range rows {
		kind := "virtual"
		if r.Physical {
			kind = "physical"
		}
		id := r.ID
		if r.Serial != "" && r.Serial != r.ID {
			id += " (serial " + r.Serial + ")"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", r.Name, r.Platform, kind, r.State, id, leaseText(r, now))
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
	if r.Physical {
		return physicalLeaseText(r, now)
	}
	l := r.Lease
	if l == nil {
		return "-"
	}
	renewed := now.Sub(l.RenewedAt).Round(time.Second)
	switch {
	case l.Suspended():
		return fmt.Sprintf("session %s, suspended since %s", l.SessionID, clockTime(l.SuspendedAt))
	case l.DeviceDied:
		return fmt.Sprintf("session %s, device died — restarts at next lease", l.SessionID)
	}
	return fmt.Sprintf("session %s, on since %s, renewed %s ago", l.SessionID, clockTime(r.Device.PoweredOnAt), renewed)
}

func physicalLeaseText(r devicebank.StatusRow, now time.Time) string {
	var parts []string
	if l := r.Lease; l != nil {
		if l.Suspended() {
			parts = append(parts, fmt.Sprintf("session %s, suspended since %s", l.SessionID, clockTime(l.SuspendedAt)))
		} else {
			parts = append(parts, fmt.Sprintf("session %s, age %s, renewed %s ago", l.SessionID,
				now.Sub(l.AcquiredAt).Round(time.Second), now.Sub(l.RenewedAt).Round(time.Second)))
		}
	}
	if res := r.Restore; res != nil {
		text := "restore pending since " + clockTime(res.EndedAt)
		if res.LastError != "" {
			text += " (" + res.LastError + ")"
		}
		parts = append(parts, text)
	}
	switch {
	case r.Absent && r.Lease != nil:
		parts = append(parts, "not connected")
	case len(parts) == 0 && !r.Wired:
		parts = append(parts, "not leasable: wireless only")
	case len(parts) == 0:
		parts = append(parts, "-")
	}
	return strings.Join(parts, " · ")
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

func newRestoreCmd() *cobra.Command {
	var forget bool
	c := &cobra.Command{
		Use:   "restore <device-id> [--forget]",
		Short: "Put back a physical device's pre-lease state, or forget a restore that cannot succeed",
		Long: `A returned physical Android device keeps a pending restore until its
pre-lease state is back (stay_on_while_plugged_in, adb reverses; no app is
ever uninstalled), and is not leasable meanwhile. An iPhone's restore has
nothing to put back and succeeds at once. The restore runs on its own,
in a detached worker spawned when the lease ends and again by later device
commands; running it by hand retries it now.

--forget drops the pending restore without touching the device, which is
leasable again: the way out for a device that refuses its restore (a phone
that forbids writing stay_on_while_plugged_in, say). Put its setting back
by hand if needed. The device is named by the ID or the serial status shows.`,
		Example: `  atelierd device restore P7SERIAL
  atelierd device restore P7SERIAL --forget`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if forget {
				return devicebank.ForgetRestore(args[0])
			}
			return devicebank.RunRestore(cmd.Context(), args[0])
		},
	}
	c.Flags().BoolVar(&forget, "forget", false, "drop the pending restore without touching the device")
	return c
}
