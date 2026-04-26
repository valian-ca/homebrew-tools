package cmds

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/credentials"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/firestore"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/outbox"
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

// checkResult is one row of `atelierd status` output.
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

// NewStatusCmd builds the `atelierd status` sub-command.
func NewStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Print health checks and exit non-zero if any FAIL",
		Long: `Run seven diagnostic checks (Lien Firebase, Âge token, Connectivité
Firestore, Watcher outbox, Heartbeat, État auth, Backlog outbox) and print
each with OK / WARN / FAIL.

Exit code: 0 if no FAIL; 1 otherwise.`,
		Args: cobra.NoArgs,
		RunE: runStatus,
	}
}

func runStatus(cmd *cobra.Command, _ []string) error {
	results := []checkResult{}

	creds, credsResult := checkCredentials()
	results = append(results, credsResult)

	statusFile, _ := status.Load()

	results = append(results, checkTokenAge(creds))
	results = append(results, checkFirestore(cmd.Context(), creds))
	results = append(results, checkWatcher(statusFile))
	results = append(results, checkHeartbeat(statusFile))
	results = append(results, checkAuthState(statusFile))
	results = append(results, checkOutboxBacklog())

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

// IsStatusFail reports whether err is the sentinel returned by status when at
// least one check is FAIL.
func IsStatusFail(err error) bool { return err != nil && err.Error() == errStatusFail.Error() }

func checkCredentials() (*credentials.Credentials, checkResult) {
	creds, err := credentials.Load()
	if err != nil {
		if err == credentials.ErrNotLinked {
			return nil, checkResult{
				name: "Lien Firebase",
				tier: tierFail,
				note: "credentials absents — exécute `atelierd link`",
			}
		}
		return nil, checkResult{
			name: "Lien Firebase",
			tier: tierFail,
			note: "lecture des credentials échouée : " + err.Error(),
		}
	}
	return creds, checkResult{
		name: "Lien Firebase",
		tier: tierOK,
		note: "lié à " + creds.Email + " (uid " + creds.UID + ")",
	}
}

func checkTokenAge(creds *credentials.Credentials) checkResult {
	if creds == nil {
		return checkResult{name: "Âge du token", tier: tierFail, note: "pas de credentials"}
	}
	now := time.Now().UTC()
	remaining := creds.IDTokenExpiresAt.Sub(now)
	if remaining > tokenWarnAfter {
		return checkResult{
			name: "Âge du token",
			tier: tierOK,
			note: fmt.Sprintf("idToken valide encore %s", remaining.Round(time.Second)),
		}
	}
	return checkResult{
		name: "Âge du token",
		tier: tierWarn,
		note: "idToken expiré — refresh attendu au prochain tick d'`atelierd run`",
	}
}

func checkFirestore(parent context.Context, creds *credentials.Credentials) checkResult {
	if creds == nil {
		return checkResult{name: "Connectivité Firestore", tier: tierFail, note: "pas de credentials"}
	}
	ctx, cancel := context.WithTimeout(parent, pingTimeout)
	defer cancel()
	if err := firestore.PingUser(ctx, creds.IDToken, creds.UID); err != nil {
		tier := tierFail
		note := "ping /users échoué : " + err.Error()
		if firestore.IsAuthLost(err) {
			note = "auth rejetée par Firestore — relance `atelierd link`"
		}
		return checkResult{name: "Connectivité Firestore", tier: tier, note: note}
	}
	return checkResult{name: "Connectivité Firestore", tier: tierOK, note: "OK"}
}

func checkWatcher(s *status.File) checkResult {
	if s == nil {
		return checkResult{
			name: "Watcher d'outbox",
			tier: tierWarn,
			note: "fichier de statut absent — `atelierd run` n'a peut-être jamais démarré (`brew services start atelierd`)",
		}
	}
	age := time.Since(s.LastTickAt)
	if age < tickStaleAfter {
		return checkResult{name: "Watcher d'outbox", tier: tierOK, note: fmt.Sprintf("dernier tick il y a %s", age.Round(time.Second))}
	}
	return checkResult{
		name: "Watcher d'outbox",
		tier: tierWarn,
		note: fmt.Sprintf("dernier tick il y a %s — le démon ne tourne peut-être pas", age.Round(time.Second)),
	}
}

func checkHeartbeat(s *status.File) checkResult {
	if s == nil {
		return checkResult{name: "Heartbeat", tier: tierWarn, note: "aucun heartbeat enregistré"}
	}
	if s.LastHeartbeatAt.IsZero() {
		return checkResult{name: "Heartbeat", tier: tierWarn, note: "aucun heartbeat encore émis"}
	}
	age := time.Since(s.LastHeartbeatAt)
	if age < heartbeatStaleAfter {
		return checkResult{name: "Heartbeat", tier: tierOK, note: fmt.Sprintf("il y a %s", age.Round(time.Second))}
	}
	return checkResult{name: "Heartbeat", tier: tierWarn, note: fmt.Sprintf("dernier heartbeat il y a %s", age.Round(time.Second))}
}

func checkAuthState(s *status.File) checkResult {
	if s == nil {
		return checkResult{name: "État auth", tier: tierWarn, note: "fichier de statut absent"}
	}
	switch s.AuthState {
	case status.AuthOk:
		return checkResult{name: "État auth", tier: tierOK, note: "ok"}
	case status.AuthLost:
		return checkResult{name: "État auth", tier: tierFail, note: "auth-lost — relance `atelierd link`"}
	default:
		return checkResult{name: "État auth", tier: tierWarn, note: "état inconnu : " + string(s.AuthState)}
	}
}

func checkOutboxBacklog() checkResult {
	count, err := outbox.Count()
	if err != nil {
		return checkResult{name: "Backlog outbox", tier: tierFail, note: "lecture impossible : " + err.Error()}
	}
	switch {
	case count > outboxBacklogFail:
		return checkResult{name: "Backlog outbox", tier: tierFail, note: fmt.Sprintf("%d fichiers en attente (>%d)", count, outboxBacklogFail)}
	case count > outboxBacklogWarn:
		return checkResult{name: "Backlog outbox", tier: tierWarn, note: fmt.Sprintf("%d fichiers en attente (>%d)", count, outboxBacklogWarn)}
	default:
		return checkResult{name: "Backlog outbox", tier: tierOK, note: fmt.Sprintf("%d fichier(s) en attente", count)}
	}
}
