package cmds

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/credentials"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/firestore"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/outbox"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/paths"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/status"
)

const (
	heartbeatStaleAfter = 90 * time.Second
	tickStaleAfter      = 90 * time.Second
	tokenWarnAfter      = 0 // expired -> WARN if refresh still possible
	outboxBacklogWarn   = 100
	outboxBacklogFail   = 1000
	pingTimeout         = 5 * time.Second
)

type checkResult struct {
	name string
	tier checkTier
	note string
}

type checkTier int

const (
	tierOK checkTier = iota
	tierWarn
	tierFail
)

func (t checkTier) label() string {
	switch t {
	case tierWarn:
		return "WARN"
	case tierFail:
		return "FAIL"
	default:
		return "OK  "
	}
}

func NewStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Print health checks and exit non-zero if any FAIL",
		Long: `Print the running version and last update check, then run eight
diagnostic checks (Firebase link, Token age, Firestore connectivity, Outbox
watcher, Heartbeat, Auth state, Outbox backlog, Rejected events), each as
OK / WARN / FAIL.

Exit code: 0 if no FAIL; 1 otherwise.`,
		Args: cobra.NoArgs,
		RunE: runStatus,
	}
}

func runStatus(cmd *cobra.Command, _ []string) error {
	results := []checkResult{}

	statusFile, _ := status.Load()
	results = append(results, checkVersion(statusFile))

	creds, credsResult := checkCredentials()
	results = append(results, credsResult)

	results = append(results, checkTokenAge(creds))
	results = append(results, checkFirestore(cmd.Context(), creds))
	results = append(results, checkWatcher(statusFile))
	results = append(results, checkHeartbeat(statusFile))
	results = append(results, checkAuthState(statusFile))
	results = append(results, checkOutboxBacklog())
	results = append(results, checkRejected())

	worst := tierOK
	for _, r := range results {
		cmd.Printf("[%s] %s — %s\n", r.tier.label(), r.name, r.note)
		if r.tier > worst {
			worst = r.tier
		}
	}

	if worst == tierFail {
		// cobra normally prints "Error:" on a returned error. We want a clean
		// exit-1 with the diagnostic lines already printed, so silence usage
		// and bypass the wrapper by exiting here through a sentinel error.
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		return errStatusFail
	}
	return nil
}

// errStatusFail is detected at the root command's PostRunE / main to exit 1
// without printing a stack trace.
var errStatusFail = fmt.Errorf("atelierd status: at least one check failed")

func IsStatusFail(err error) bool { return err != nil && err.Error() == errStatusFail.Error() }

func checkVersion(s *status.File) checkResult {
	return versionCheck(s, Version, time.Now())
}

// versionCheck reports the daemon's version next to the version of this
// binary. The two differ after a `brew upgrade` until the daemon restarts, and
// the status file keeps the last daemon's version after it stops — reporting
// that alone once made a daemon stopped for a day look healthy on the old
// version. A stale or mismatched daemon is a WARN; the watcher and heartbeat
// rows carry the FAIL-worthy signal.
func versionCheck(s *status.File, binary string, now time.Time) checkResult {
	const hint = "start it: brew services restart atelierd — if it stays stopped: launchctl kickstart -k gui/$(id -u)/sh.brew.atelierd"
	if s == nil || s.Version == "" {
		note := binary + " (daemon has not written a status file yet)"
		if binary == devVersion {
			note = binary + " (dev build — auto-update disabled; no status file yet)"
		}
		return checkResult{name: "Version", tier: tierOK, note: note}
	}

	daemon := s.Version
	if age := now.Sub(s.LastTickAt); s.LastTickAt.IsZero() || age >= tickStaleAfter {
		seen := "never ticked"
		if !s.LastTickAt.IsZero() {
			seen = "last seen " + age.Round(time.Second).String() + " ago"
		}
		return checkResult{
			name: "Version",
			tier: tierWarn,
			note: fmt.Sprintf("daemon not running (status file from %s, %s); installed binary %s — %s", daemon, seen, binary, hint),
		}
	}

	if daemon != binary && daemon != devVersion && binary != devVersion {
		return checkResult{
			name: "Version",
			tier: tierWarn,
			note: fmt.Sprintf("daemon runs %s but installed binary is %s — it restarts onto it after its next update check, or now with: brew services restart atelierd", daemon, binary),
		}
	}

	if daemon == devVersion {
		return checkResult{name: "Version", tier: tierOK, note: daemon + " (dev build — auto-update disabled)"}
	}
	if s.LastUpdateCheckAt.IsZero() {
		return checkResult{name: "Version", tier: tierOK, note: daemon + " (no update check yet)"}
	}
	return checkResult{
		name: "Version",
		tier: tierOK,
		note: fmt.Sprintf("%s (last update check %s ago)", daemon, now.Sub(s.LastUpdateCheckAt).Round(time.Second)),
	}
}

func checkCredentials() (*credentials.Credentials, checkResult) {
	creds, err := credentials.Load()
	if err != nil {
		if err == credentials.ErrNotLinked {
			return nil, checkResult{
				name: "Firebase link",
				tier: tierFail,
				note: "no credentials — run `atelierd link`",
			}
		}
		return nil, checkResult{
			name: "Firebase link",
			tier: tierFail,
			note: "credentials read failed: " + err.Error(),
		}
	}
	return creds, checkResult{
		name: "Firebase link",
		tier: tierOK,
		note: "linked as " + creds.Email + " (uid " + creds.UID + ")",
	}
}

func checkTokenAge(creds *credentials.Credentials) checkResult {
	if creds == nil {
		return checkResult{name: "Token age", tier: tierFail, note: "no credentials"}
	}
	now := time.Now().UTC()
	remaining := creds.IDTokenExpiresAt.Sub(now)
	if remaining > tokenWarnAfter {
		return checkResult{
			name: "Token age",
			tier: tierOK,
			note: fmt.Sprintf("idToken valid for %s", remaining.Round(time.Second)),
		}
	}
	return checkResult{
		name: "Token age",
		tier: tierWarn,
		note: "idToken expired — refresh expected on next `atelierd run` tick",
	}
}

func checkFirestore(parent context.Context, creds *credentials.Credentials) checkResult {
	if creds == nil {
		return checkResult{name: "Firestore connectivity", tier: tierFail, note: "no credentials"}
	}
	ctx, cancel := context.WithTimeout(parent, pingTimeout)
	defer cancel()
	if err := firestore.PingUser(ctx, creds.IDToken, creds.UID); err != nil {
		note := "ping /users failed: " + err.Error()
		if firestore.IsAuthLost(err) {
			note = "auth rejected by Firestore — re-run `atelierd link`"
		}
		return checkResult{name: "Firestore connectivity", tier: tierFail, note: note}
	}
	return checkResult{name: "Firestore connectivity", tier: tierOK, note: "OK"}
}

func checkWatcher(s *status.File) checkResult {
	if s == nil {
		return checkResult{
			name: "Outbox watcher",
			tier: tierWarn,
			note: "status file missing — `atelierd run` may have never started (`brew services start atelierd`)",
		}
	}
	age := time.Since(s.LastTickAt)
	if age < tickStaleAfter {
		return checkResult{name: "Outbox watcher", tier: tierOK, note: fmt.Sprintf("last tick %s ago", age.Round(time.Second))}
	}
	return checkResult{
		name: "Outbox watcher",
		tier: tierWarn,
		note: fmt.Sprintf("last tick %s ago — the daemon may not be running", age.Round(time.Second)),
	}
}

func checkHeartbeat(s *status.File) checkResult {
	if s == nil {
		return checkResult{name: "Heartbeat", tier: tierWarn, note: "no heartbeat recorded"}
	}
	if s.LastHeartbeatAt.IsZero() {
		return checkResult{name: "Heartbeat", tier: tierWarn, note: "no heartbeat sent yet"}
	}
	age := time.Since(s.LastHeartbeatAt)
	if age < heartbeatStaleAfter {
		return checkResult{name: "Heartbeat", tier: tierOK, note: fmt.Sprintf("%s ago", age.Round(time.Second))}
	}
	return checkResult{name: "Heartbeat", tier: tierWarn, note: fmt.Sprintf("last heartbeat %s ago", age.Round(time.Second))}
}

func checkAuthState(s *status.File) checkResult {
	if s == nil {
		return checkResult{name: "Auth state", tier: tierWarn, note: "status file missing"}
	}
	switch s.AuthState {
	case status.AuthOk:
		return checkResult{name: "Auth state", tier: tierOK, note: "ok"}
	case status.AuthLost:
		return checkResult{name: "Auth state", tier: tierFail, note: "auth-lost — re-run `atelierd link`"}
	default:
		return checkResult{name: "Auth state", tier: tierWarn, note: "unknown state: " + string(s.AuthState)}
	}
}

func checkOutboxBacklog() checkResult {
	count, err := outbox.Count()
	if err != nil {
		return checkResult{name: "Outbox backlog", tier: tierFail, note: "read failed: " + err.Error()}
	}
	switch {
	case count > outboxBacklogFail:
		return checkResult{name: "Outbox backlog", tier: tierFail, note: fmt.Sprintf("%d files pending (>%d)", count, outboxBacklogFail)}
	case count > outboxBacklogWarn:
		return checkResult{name: "Outbox backlog", tier: tierWarn, note: fmt.Sprintf("%d files pending (>%d)", count, outboxBacklogWarn)}
	default:
		return checkResult{name: "Outbox backlog", tier: tierOK, note: fmt.Sprintf("%d file(s) pending", count)}
	}
}

// checkRejected counts *.json.rejected files — events Firestore refused with a
// 403. Not an auth check: the token is valid, so it never advises
// `atelierd link`; it prints the command that clears the quarantine.
func checkRejected() checkResult {
	count, err := outbox.CountRejected()
	if err != nil {
		return checkResult{name: "Rejected events", tier: tierWarn, note: "read failed: " + err.Error()}
	}
	if count == 0 {
		return checkResult{name: "Rejected events", tier: tierOK, note: "none"}
	}
	return checkResult{
		name: "Rejected events",
		tier: tierWarn,
		note: fmt.Sprintf("%d event(s) quarantined by Firestore (permission) — see ~/.atelier/atelierd.log; clear with: %s", count, rejectedCleanupCommand(paths.Outbox())),
	}
}

func rejectedCleanupCommand(outboxDir string) string {
	return "rm " + shellQuote(outboxDir) + "/*.json.rejected"
}

func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"$`\\!*?[]{}()<>|&;#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
