package cmds

import (
	"context"
	"fmt"
	mathrand "math/rand"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/cobra"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/credentials"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/firebaseauth"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/firestore"
	atelierlog "github.com/valian-ca/homebrew-tools/internal/atelierd/log"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/outbox"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/paths"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/status"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/ulid"
)

// Version is overwritten via -ldflags by cmd/atelierd/main.go. Kept here so
// the run loop can stamp it into the status file without a circular import.
var Version = "dev"

const (
	heartbeatInterval = 60 * time.Second
	statusInterval    = 30 * time.Second
	reconcileInterval = 5 * time.Second
	refreshLeadTime   = 5 * time.Minute
	// refreshPollInterval is how often the refresher re-checks the wall-clock
	// expiry of the idToken. We poll instead of using one long time.After()
	// because Go timers track the monotonic clock, which is frozen during
	// macOS sleep — at wake the goroutine resumes and the next tick re-reads
	// time.Now() (wall-clock) so a token that expired during sleep is caught
	// and refreshed before any Firestore call has the chance to 401.
	refreshPollInterval = 30 * time.Second
	authLostReminder    = 5 * time.Minute

	shipBatchMax  = 50
	shipBatchTime = 1 * time.Second

	backoffMin = 1 * time.Second
	backoffCap = 60 * time.Second
)

// NewRunCmd builds the `atelierd run` sub-command — the long-lived daemon
// loop launched by `brew services start atelierd` via launchd.
func NewRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Run the atelierd daemon (shipper + heartbeat + refresher)",
		Long: `Long-lived loop. Watches ~/.atelier/outbox/ and ships every event to
Firestore /events/{ulid}. Refreshes the idToken before expiry. Writes a
heartbeat to /users/{uid}.lastHeartbeat every 60s. Writes a status snapshot to
~/.atelier/status every 30s. Watches ~/.atelier/credentials and reloads on
re-link without requiring brew services restart.

On Firebase Auth 401/403, enters "auth-lost" mode: ship + heartbeat + refresh
loops pause; the outbox accumulates; the status file marks authState=auth-lost.
Recovery: atelierd link — the daemon picks up the new credentials automatically
via the fsnotify watcher and exits auth-lost mode within seconds.`,
		Args: cobra.NoArgs,
		RunE: runRun,
	}
}

func runRun(cmd *cobra.Command, _ []string) error {
	if err := atelierlog.Init(); err != nil {
		// Log init failure is non-fatal — the package falls back to stderr.
		cmd.PrintErrln("(log init: " + err.Error() + ")")
	}
	defer atelierlog.Close()

	creds, err := credentials.Load()
	if err != nil {
		atelierlog.Error("startup: credentials load failed", "err", err.Error())
		return err
	}

	host, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("resolve hostname: %w", err)
	}

	if err := paths.EnsureDir(paths.Outbox()); err != nil {
		return fmt.Errorf("ensure outbox dir: %w", err)
	}

	state := &runState{
		creds:     creds,
		host:      host,
		authState: status.AuthOk,
		lastTick:  time.Now().UTC(),
	}

	// Initial status file write so `atelierd status` doesn't WARN on first
	// invocation before the writer goroutine has had a chance to tick.
	if err := writeStatusSnapshot(state); err != nil {
		atelierlog.Warn("startup: initial status write failed", "err", err.Error())
	}

	rootCtx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	// Trap SIGINT / SIGTERM so launchd can stop the daemon cleanly.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		atelierlog.Info("signal received, shutting down", "signal", sig.String())
		cancel()
	}()

	// Proactive refresh if the loaded credentials are already (or near) expired —
	// prevents a heartbeat-before-refresh race that would otherwise trip
	// auth-lost on the first 60 s heartbeat tick after a long machine-off.
	refreshOnBoot(rootCtx, state)

	atelierlog.Info("atelierd run started", "uid", state.snapshot().UID, "host", host, "version", Version)

	var wg sync.WaitGroup
	wg.Add(5)
	go func() { defer wg.Done(); shipperLoop(rootCtx, state) }()
	go func() { defer wg.Done(); refresherLoop(rootCtx, state) }()
	go func() { defer wg.Done(); heartbeatLoop(rootCtx, state) }()
	go func() { defer wg.Done(); statusWriterLoop(rootCtx, state) }()
	go func() { defer wg.Done(); credentialsWatcherLoop(rootCtx, state) }()

	wg.Wait()
	atelierlog.Info("atelierd run stopped")
	// Write a final status snapshot capturing shutdown time.
	state.touchTick()
	_ = writeStatusSnapshot(state)
	return nil
}

// runState is the mutex-guarded shared state across run's goroutines.
type runState struct {
	mu             sync.RWMutex
	creds          *credentials.Credentials
	host           string
	authState      status.AuthState
	lastTick       time.Time
	lastHeartbeat  time.Time
	lastShip       time.Time
	outboxBacklog  int
	currentBackoff time.Duration
}

func (s *runState) snapshot() *status.File {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return &status.File{
		Version:          Version,
		UID:              s.creds.UID,
		Host:             s.host,
		LastTickAt:       s.lastTick,
		LastHeartbeatAt:  s.lastHeartbeat,
		LastShipAt:       s.lastShip,
		OutboxBacklog:    s.outboxBacklog,
		AuthState:        s.authState,
		IDTokenExpiresAt: s.creds.IDTokenExpiresAt,
	}
}

func (s *runState) currentCreds() *credentials.Credentials {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c := *s.creds
	return &c
}

func (s *runState) updateCreds(c *credentials.Credentials) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.creds = c
}

func (s *runState) markAuthLost(reason string) {
	s.mu.Lock()
	wasOk := s.authState == status.AuthOk
	s.authState = status.AuthLost
	s.mu.Unlock()
	if wasOk {
		atelierlog.Error("auth-lost: ship + heartbeat + refresh paused; re-run atelierd link to recover", "reason", reason)
	}
}

// clearAuthLost flips authState back to ok and resets retry backoff. Called
// by the credentials watcher after a successful re-link rewrites
// ~/.atelier/credentials. The ship/heartbeat/refresh loops poll authState on
// each iteration and will resume normal operation within a few seconds.
func (s *runState) clearAuthLost(reason string) {
	s.mu.Lock()
	wasLost := s.authState == status.AuthLost
	s.authState = status.AuthOk
	s.currentBackoff = 0
	s.mu.Unlock()
	if wasLost {
		atelierlog.Info("auth-lost cleared: ship + heartbeat + refresh resuming", "reason", reason)
	}
}

func (s *runState) isAuthLost() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.authState == status.AuthLost
}

func (s *runState) touchTick() {
	s.mu.Lock()
	s.lastTick = time.Now().UTC()
	s.mu.Unlock()
}

func (s *runState) markShipped(when time.Time, backlog int) {
	s.mu.Lock()
	s.lastShip = when
	s.outboxBacklog = backlog
	s.currentBackoff = 0 // reset on success
	s.mu.Unlock()
}

func (s *runState) markHeartbeat(when time.Time) {
	s.mu.Lock()
	s.lastHeartbeat = when
	s.mu.Unlock()
}

func (s *runState) nextBackoff() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.currentBackoff == 0 {
		s.currentBackoff = backoffMin
	} else {
		s.currentBackoff *= 2
		if s.currentBackoff > backoffCap {
			s.currentBackoff = backoffCap
		}
	}
	// ±20 % jitter
	jitter := float64(s.currentBackoff) * 0.2 * (mathrand.Float64()*2 - 1) //nolint:gosec // not security-sensitive
	d := s.currentBackoff + time.Duration(jitter)
	if d < backoffMin {
		d = backoffMin
	}
	return d
}

func writeStatusSnapshot(s *runState) error {
	return status.Save(s.snapshot())
}

// refreshOnBoot synchronously refreshes the idToken if the loaded credentials
// are within refreshLeadTime of expiry (or already past). Without this, a long
// machine-off can leave creds expired at boot, and the heartbeat goroutine
// (which fires immediately) would race the refresher and trip auth-lost on
// the first 401.
func refreshOnBoot(ctx context.Context, state *runState) {
	creds := state.currentCreds()
	if !shouldRefreshNow(creds.IDTokenExpiresAt, refreshLeadTime, time.Now()) {
		return
	}
	if err := performRefresh(ctx, state); err != nil && !firebaseauth.IsAuthLost(err) {
		atelierlog.Warn("startup refresh: transient error, continuing", "err", err.Error())
	}
}

// withAuthRecovery runs op (a Firestore call carrying the current idToken).
// If op returns 401/403, it attempts a single proactive refresh of the
// idToken and retries op once with the freshly minted token. Only escalates
// to auth-lost when:
//
//  1. The refresh itself returns 401/403 — the refresh token is truly revoked.
//  2. The retry still returns 401/403 even with a fresh idToken.
//
// This unblocks the common post-sleep failure mode where macOS froze the
// monotonic-clock refresh timer for hours, the idToken silently expired, and
// the next Firestore write would otherwise irreversibly trip auth-lost.
func withAuthRecovery(ctx context.Context, state *runState, opName string, op func(idToken string) error) error {
	creds := state.currentCreds()
	err := op(creds.IDToken)
	if err == nil {
		return nil
	}
	if !firestore.IsAuthLost(err) && !firebaseauth.IsAuthLost(err) {
		return err // transient — let the caller back off
	}

	atelierlog.Warn(opName+": got 401, attempting reactive token refresh before declaring auth-lost", "err", err.Error())
	if rerr := performRefresh(ctx, state); rerr != nil {
		// performRefresh already invoked markAuthLost on a 401/403 from
		// the securetoken endpoint. On a transient refresh failure (5xx,
		// network) we leave authState untouched so the next reconcile or
		// refresher tick can retry.
		return rerr
	}

	// Retry the op once with the fresh idToken.
	creds = state.currentCreds()
	err = op(creds.IDToken)
	if err == nil {
		atelierlog.Info(opName + ": recovered after reactive refresh")
		return nil
	}
	if firestore.IsAuthLost(err) || firebaseauth.IsAuthLost(err) {
		state.markAuthLost(opName + ": still 401 after reactive refresh: " + err.Error())
	}
	return err
}

// ============================================================================
// Goroutine 1 — shipperLoop
// ============================================================================

func shipperLoop(ctx context.Context, state *runState) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		atelierlog.Error("shipper: fsnotify init failed; falling back to periodic reconcile only", "err", err.Error())
	} else {
		defer watcher.Close()
		if err := watcher.Add(paths.Outbox()); err != nil {
			atelierlog.Error("shipper: fsnotify add failed", "err", err.Error())
			watcher = nil
		}
	}

	reconcile := time.NewTicker(reconcileInterval)
	defer reconcile.Stop()

	// Initial pass — ship anything queued before the daemon started.
	tryShip(ctx, state)

	for {
		select {
		case <-ctx.Done():
			return
		case <-reconcile.C:
			tryShip(ctx, state)
		case ev, ok := <-watcherEvents(watcher):
			if !ok {
				continue
			}
			if shouldShipOnEvent(ev) {
				tryShip(ctx, state)
			}
		}
	}
}

func watcherEvents(w *fsnotify.Watcher) <-chan fsnotify.Event {
	if w == nil {
		// Return a nil channel so the select case never fires.
		return nil
	}
	return w.Events
}

func shouldShipOnEvent(ev fsnotify.Event) bool {
	if ev.Op&(fsnotify.Create|fsnotify.Rename|fsnotify.Write) == 0 {
		return false
	}
	return strings.HasSuffix(ev.Name, ".json")
}

// tryShip lists the outbox, batches up to shipBatchMax (or shipBatchTime),
// and ships each batch. Updates state at the end.
func tryShip(ctx context.Context, state *runState) {
	state.touchTick()
	if state.isAuthLost() {
		updateBacklog(state)
		return
	}
	files, err := outbox.List()
	if err != nil {
		atelierlog.Error("shipper: list outbox failed", "err", err.Error())
		return
	}
	if len(files) == 0 {
		updateBacklog(state)
		return
	}

	for len(files) > 0 {
		batchSize := shipBatchMax
		if len(files) < batchSize {
			batchSize = len(files)
		}
		batch := files[:batchSize]
		files = files[batchSize:]

		if err := shipBatch(ctx, state, batch); err != nil {
			if firestore.IsAuthLost(err) || firebaseauth.IsAuthLost(err) {
				state.markAuthLost("ship: " + err.Error())
				updateBacklog(state)
				return
			}
			// Transient — back off and retry next reconcile.
			delay := state.nextBackoff()
			atelierlog.Warn("shipper: batch failed, backing off", "err", err.Error(), "next", delay.String())
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			updateBacklog(state)
			return
		}
	}

	updateBacklog(state)
}

func updateBacklog(state *runState) {
	count, err := outbox.Count()
	if err != nil {
		return
	}
	state.mu.Lock()
	state.outboxBacklog = count
	state.mu.Unlock()
}

func shipBatch(ctx context.Context, state *runState, files []string) error {
	creds := state.currentCreds()
	docs := make([]*firestore.EventDoc, 0, len(files))
	keep := make([]string, 0, len(files))

	for _, f := range files {
		env, err := outbox.Read(f)
		if err != nil {
			atelierlog.Warn("shipper: skipping unreadable outbox file", "file", filepath.Base(f), "err", err.Error())
			// Move it aside so we don't loop forever — append .corrupt suffix.
			_ = os.Rename(f, f+".corrupt")
			continue
		}
		ts, err := ulid.Timestamp(env.ULID)
		if err != nil {
			atelierlog.Warn("shipper: bad ULID in outbox file", "file", filepath.Base(f), "err", err.Error())
			_ = os.Rename(f, f+".corrupt")
			continue
		}
		docs = append(docs, &firestore.EventDoc{
			ULID:            env.ULID,
			Type:            env.Type,
			ClaudeSessionID: env.ClaudeSessionID,
			UID:             creds.UID,
			Host:            state.host,
			TS:              ts,
			Payload:         env.Payload,
		})
		keep = append(keep, f)
	}

	if len(docs) == 0 {
		return nil
	}

	deadline := time.Now().Add(shipBatchTime)
	commitCtx, cancel := context.WithDeadline(ctx, deadline.Add(10*time.Second))
	defer cancel()

	err := withAuthRecovery(ctx, state, "ship", func(idToken string) error {
		return firestore.CommitEvents(commitCtx, idToken, docs)
	})
	if err != nil {
		return err
	}

	for _, f := range keep {
		if err := outbox.Delete(f); err != nil {
			atelierlog.Warn("shipper: delete after ship failed", "file", filepath.Base(f), "err", err.Error())
		}
	}
	state.markShipped(time.Now().UTC(), 0) // backlog will be re-counted post-loop
	atelierlog.Info("shipper: shipped batch", "count", len(docs))
	return nil
}

// ============================================================================
// Goroutine 2 — refresherLoop
// ============================================================================

func refresherLoop(ctx context.Context, state *runState) {
	tick := time.NewTicker(refreshPollInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}

		// Auth-lost can be cleared by the credentials watcher after a re-link;
		// keep polling so we resume cleanly when that happens. The ship and
		// heartbeat loops also poll authState on each iteration.
		if state.isAuthLost() {
			continue
		}

		creds := state.currentCreds()
		// Wall-clock check — robust to macOS sleep, where Go's monotonic-clock
		// timers freeze. At wake, this comparison sees the real expiry and
		// triggers a refresh on the very next tick.
		if !shouldRefreshNow(creds.IDTokenExpiresAt, refreshLeadTime, time.Now()) {
			continue
		}

		if err := performRefresh(ctx, state); err != nil {
			if firebaseauth.IsAuthLost(err) {
				// markAuthLost already invoked by performRefresh.
				continue
			}
			delay := state.nextBackoff()
			atelierlog.Warn("refresher: transient error, backing off", "err", err.Error(), "next", delay.String())
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
		}
	}
}

// shouldRefreshNow reports whether the idToken should be refreshed at `now`,
// using a wall-clock comparison. Pure function so unit tests can drive it
// without touching time.Now() in test code.
func shouldRefreshNow(expiresAt time.Time, leadTime time.Duration, now time.Time) bool {
	return now.Add(leadTime).After(expiresAt) || now.Add(leadTime).Equal(expiresAt)
}

// performRefresh is the inner step of refresherLoop: trade the current refresh
// token for a new idToken, persist credentials, update state. Used both by
// refresherLoop (proactive, scheduled) and by withAuthRecovery (reactive, after
// an unexpected 401 from Firestore). Returns the underlying firebaseauth error
// untouched so callers can distinguish auth-lost from transient.
func performRefresh(ctx context.Context, state *runState) error {
	creds := state.currentCreds()
	refreshCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := firebaseauth.RefreshIDToken(refreshCtx, creds.RefreshToken)
	if err != nil {
		if firebaseauth.IsAuthLost(err) {
			state.markAuthLost("refresh: " + err.Error())
		}
		return err
	}
	updated := &credentials.Credentials{
		UID:              creds.UID,
		Email:            creds.Email,
		IDToken:          res.IDToken,
		RefreshToken:     res.RefreshToken,
		IDTokenExpiresAt: res.IDTokenExpiresAt,
	}
	if perr := credentials.Save(updated); perr != nil {
		// Persist failure is logged but doesn't unmark the in-memory update —
		// next refresh will overwrite the disk copy anyway.
		atelierlog.Warn("refresher: persist failed", "err", perr.Error())
	}
	state.updateCreds(updated)
	atelierlog.Info("idToken refreshed", "next_expiry", res.IDTokenExpiresAt.Format(time.RFC3339))
	return nil
}

// ============================================================================
// Goroutine 3 — heartbeatLoop
// ============================================================================

func heartbeatLoop(ctx context.Context, state *runState) {
	tick := time.NewTicker(heartbeatInterval)
	defer tick.Stop()
	// Fire one immediately so `atelierd status` shows a fresh heartbeat after
	// a brew services start.
	doHeartbeat(ctx, state)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			doHeartbeat(ctx, state)
		}
	}
}

func doHeartbeat(ctx context.Context, state *runState) {
	if state.isAuthLost() {
		return
	}
	uid := state.currentCreds().UID
	hbCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err := withAuthRecovery(ctx, state, "heartbeat", func(idToken string) error {
		return firestore.SetUserHeartbeat(hbCtx, idToken, uid)
	})
	if err != nil {
		// withAuthRecovery already marked auth-lost where appropriate; here we
		// just log and rely on the next heartbeat tick to retry.
		atelierlog.Warn("heartbeat: write failed", "err", err.Error())
		return
	}
	state.markHeartbeat(time.Now().UTC())
}

// ============================================================================
// Goroutine 4 — statusWriterLoop
// ============================================================================

func statusWriterLoop(ctx context.Context, state *runState) {
	tick := time.NewTicker(statusInterval)
	defer tick.Stop()
	reminder := time.NewTicker(authLostReminder)
	defer reminder.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			state.touchTick()
			updateBacklog(state)
			if err := writeStatusSnapshot(state); err != nil {
				atelierlog.Warn("status writer: save failed", "err", err.Error())
			}
		case <-reminder.C:
			if state.isAuthLost() {
				atelierlog.Warn("auth-lost reminder: re-run `atelierd link` — the daemon will pick up the new credentials automatically")
			}
		}
	}
}

// ============================================================================
// Goroutine 5 — credentialsWatcherLoop
// ============================================================================

// credentialsWatcherLoop watches ~/.atelier/ for changes to the credentials
// file. When `atelierd link` rewrites it (atomic os.Rename via
// credentials.Save), we reload the new tokens, clear auth-lost mode if set,
// and let the ship/heartbeat/refresh loops pick the new idToken on their next
// poll. This removes the need for `brew services restart atelierd` after a
// re-link.
//
// We watch the parent directory (not the file itself) because os.Rename
// replaces the inode — fsnotify on the file path would lose its subscription
// after the first replacement.
func credentialsWatcherLoop(ctx context.Context, state *runState) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		atelierlog.Error("credentials-watcher: fsnotify init failed", "err", err.Error())
		<-ctx.Done()
		return
	}
	defer watcher.Close()

	if err := watcher.Add(paths.MustRoot()); err != nil {
		atelierlog.Error("credentials-watcher: fsnotify add failed", "err", err.Error())
		<-ctx.Done()
		return
	}

	credPath := paths.Credentials()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-watcher.Events:
			if !ok {
				return
			}
			if !shouldHandleCredentialsEvent(ev, credPath) {
				continue
			}
			// Brief debounce — atomic rename can fire Create + Rename in
			// quick succession, and `atelierd link` writes the tempfile
			// before renaming it into place.
			drainEvents(watcher.Events, 200*time.Millisecond)
			handleCredentialsChange(state)
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			atelierlog.Warn("credentials-watcher: error", "err", err.Error())
		}
	}
}

// shouldHandleCredentialsEvent filters fsnotify events on the ~/.atelier/
// directory down to the ones that signal a credentials write — atomic
// os.Rename surfaces as Create on the destination, plain writes surface as
// Write. Pure function for unit testing.
func shouldHandleCredentialsEvent(ev fsnotify.Event, credPath string) bool {
	if ev.Name != credPath {
		return false
	}
	return ev.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Rename) != 0
}

// drainEvents collects any extra events that arrive within the debounce
// window. Without this, a single re-link writing the tempfile and then
// renaming it could trigger two reloads back-to-back.
func drainEvents(ch <-chan fsnotify.Event, window time.Duration) {
	deadline := time.NewTimer(window)
	defer deadline.Stop()
	for {
		select {
		case <-ch:
			// Reset the window — keep draining as long as events keep coming.
			if !deadline.Stop() {
				<-deadline.C
			}
			deadline.Reset(window)
		case <-deadline.C:
			return
		}
	}
}

// handleCredentialsChange reloads the credentials file and updates the run
// state. Called by the watcher after a debounced fsnotify event.
func handleCredentialsChange(state *runState) {
	creds, err := credentials.Load()
	if err != nil {
		// File may have been transiently absent during atomic rename; on a
		// real `atelierd unlink`, ErrNotLinked is expected and we keep the
		// in-memory creds (the run loop will continue using them until they
		// expire — at which point auth-lost takes over normally).
		atelierlog.Warn("credentials-watcher: reload failed", "err", err.Error())
		return
	}
	state.updateCreds(creds)
	state.clearAuthLost("credentials reloaded after re-link")
	atelierlog.Info("credentials-watcher: credentials reloaded", "uid", creds.UID, "email", creds.Email)
}
