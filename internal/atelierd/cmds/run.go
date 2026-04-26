package cmds

import (
	"context"
	"errors"
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
	authLostReminder  = 5 * time.Minute

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
~/.atelier/status every 30s.

On Firebase Auth 401/403, enters "auth-lost" mode: ship + heartbeat + refresh
loops stop; the outbox accumulates; the status file marks authState=auth-lost.
Recovery: atelierd unlink && atelierd link, then brew services restart atelierd.`,
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

	atelierlog.Info("atelierd run started", "uid", state.snapshot().UID, "host", host, "version", Version)

	var wg sync.WaitGroup
	wg.Add(4)
	go func() { defer wg.Done(); shipperLoop(rootCtx, state) }()
	go func() { defer wg.Done(); refresherLoop(rootCtx, state) }()
	go func() { defer wg.Done(); heartbeatLoop(rootCtx, state) }()
	go func() { defer wg.Done(); statusWriterLoop(rootCtx, state) }()

	wg.Wait()
	atelierlog.Info("atelierd run stopped")
	// Write a final status snapshot capturing shutdown time.
	state.touchTick()
	_ = writeStatusSnapshot(state)
	return nil
}

// runState is the mutex-guarded shared state across run's goroutines.
type runState struct {
	mu               sync.RWMutex
	creds            *credentials.Credentials
	host             string
	authState        status.AuthState
	lastTick         time.Time
	lastHeartbeat    time.Time
	lastShip         time.Time
	outboxBacklog    int
	currentBackoff   time.Duration
	authLostNotifyAt time.Time
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
	s.authLostNotifyAt = time.Now().UTC()
	s.mu.Unlock()
	if wasOk {
		atelierlog.Error("auth-lost: ship + heartbeat + refresh loops stopping; relancer atelierd link", "reason", reason)
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

	if err := firestore.CommitEvents(commitCtx, creds.IDToken, docs); err != nil {
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
	for {
		if state.isAuthLost() {
			// Wait for shutdown — auth-lost is terminal in this run.
			<-ctx.Done()
			return
		}
		creds := state.currentCreds()
		wait := time.Until(creds.IDTokenExpiresAt) - refreshLeadTime
		if wait < 0 {
			wait = 0
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		if state.isAuthLost() {
			continue
		}

		creds = state.currentCreds()
		refreshCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		res, err := firebaseauth.RefreshIDToken(refreshCtx, creds.RefreshToken)
		cancel()
		if err != nil {
			if firebaseauth.IsAuthLost(err) {
				state.markAuthLost("refresh: " + err.Error())
				continue
			}
			delay := state.nextBackoff()
			atelierlog.Warn("refresher: transient error, backing off", "err", err.Error(), "next", delay.String())
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			continue
		}

		updated := &credentials.Credentials{
			UID:              creds.UID,
			Email:            creds.Email,
			IDToken:          res.IDToken,
			RefreshToken:     res.RefreshToken,
			IDTokenExpiresAt: res.IDTokenExpiresAt,
		}
		if err := credentials.Save(updated); err != nil {
			atelierlog.Error("refresher: persist failed", "err", err.Error())
			continue
		}
		state.updateCreds(updated)
		atelierlog.Info("refresher: idToken refreshed", "next_expiry", res.IDTokenExpiresAt.Format(time.RFC3339))
	}
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
	creds := state.currentCreds()
	hbCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := firestore.SetUserHeartbeat(hbCtx, creds.IDToken, creds.UID); err != nil {
		if firestore.IsAuthLost(err) {
			state.markAuthLost("heartbeat: " + err.Error())
			return
		}
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
				atelierlog.Warn("auth-lost reminder: relance `atelierd link` puis `brew services restart atelierd`")
			}
		}
	}
}

// ensure errors.As compiles even when no auth-lost branch is taken
var _ = errors.As
