package cmds

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"time"

	atelierlog "github.com/valian-ca/homebrew-tools/internal/atelierd/log"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/outbox"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/paths"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/transcript"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/ulid"
)

// sessionPollInterval paces the discovery of freshly registered sessions.
// Var rather than const so integration tests can drive it with a sub-second
// tick.
var sessionPollInterval = 5 * time.Second

// treePollInterval paces the read of an active session tree. A poll rather
// than fsnotify: on darwin a directory watch never wakes on appends to the
// files inside it, and Workflow agents write three directories down.
var treePollInterval = 2 * time.Second

// sessionIdleTimeout bounds reading to active sessions: a session tree with
// no consumed line for this long releases its reader, and dormant states on
// disk don't get one at startup. Var so tests can shrink it.
var sessionIdleTimeout = 30 * time.Minute

// dormantScanInterval paces the revival check of dormant sessions — a stat of
// each transcript of a dormant tree per scan, instead of a read every poll.
var dormantScanInterval = 30 * time.Second

// stateGCInterval paces the orphan-state purge after the startup run. Claude
// Code deletes transcripts after ~30 days, so daily is more than enough.
var stateGCInterval = 24 * time.Hour

// maxHeartbeatSkew mirrors the `ts <= request.time + 5m` rule on /events: a
// heartbeat further in the future (a skewed transcript clock) would only be
// quarantined.
const maxHeartbeatSkew = 5 * time.Minute

// readHook runs between a tree read and its state save. Test-only seam for
// interleaving a concurrent `atelierd emit` registration.
var readHook func()

func isSessionActive(s *transcript.State, now time.Time) bool {
	return now.Sub(s.LastActivityAt) < sessionIdleTimeout
}

// hasUnconsumedBytes reports whether any transcript of the tree holds bytes
// its offset hasn't consumed. Size below the offset (truncation) counts too.
func hasUnconsumedBytes(s *transcript.State) bool {
	for _, f := range s.TreeFiles() {
		stat, err := os.Stat(f)
		if err != nil {
			continue
		}
		if stat.Size() != s.Offset(f) {
			return true
		}
	}
	return false
}

func shouldSpawnReader(s *transcript.State, now time.Time) bool {
	return isSessionActive(s, now) || hasUnconsumedBytes(s)
}

func sessionsManagerLoop(ctx context.Context, _ *runState) {
	if err := paths.EnsureDir(transcript.SessionsDir()); err != nil {
		atelierlog.Error("sessions-manager: ensure sessions dir failed", "err", err.Error())
		<-ctx.Done()
		return
	}

	readers := map[string]context.CancelFunc{}
	var wg sync.WaitGroup
	var mu sync.Mutex

	spawn := func(state *transcript.State) bool {
		mu.Lock()
		if _, exists := readers[state.ClaudeSessionID]; exists {
			mu.Unlock()
			return false
		}
		rctx, cancel := context.WithCancel(ctx)
		readers[state.ClaudeSessionID] = cancel
		mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				mu.Lock()
				delete(readers, state.ClaudeSessionID)
				mu.Unlock()
			}()
			runSessionReader(rctx, state.ClaudeSessionID)
		}()
		return true
	}

	// Initial scan: rehydrate only the sessions worth reading — active ones,
	// plus dormant ones whose tree grew while the daemon was down. The rest
	// stay on disk for the dormant scan.
	states, err := transcript.ListStates()
	if err != nil {
		atelierlog.Warn("sessions-manager: initial scan failed", "err", err.Error())
	}
	now := time.Now()
	for _, s := range states {
		if shouldSpawnReader(s, now) {
			spawn(s)
		}
	}

	// No fsnotify on SessionsDir: watching a directory on kqueue opens one fd
	// per file inside it, so the watch alone would pin as many descriptors as
	// there are state files on disk (the 30-day session backlog).
	tick := time.NewTicker(sessionPollInterval)
	defer tick.Stop()
	dormantTick := time.NewTicker(dormantScanInterval)
	defer dormantTick.Stop()

	for {
		select {
		case <-ctx.Done():
			mu.Lock()
			for _, cancel := range readers {
				cancel()
			}
			mu.Unlock()
			wg.Wait()
			return
		case <-tick.C:
			// Active states only: stat-ing every dormant tree here would put
			// the whole fleet back on a 5 s cadence.
			states, err := transcript.ListStates()
			if err != nil {
				continue
			}
			now := time.Now()
			for _, s := range states {
				if isSessionActive(s, now) {
					spawn(s)
				}
			}
		case <-dormantTick.C:
			states, err := transcript.ListStates()
			if err != nil {
				continue
			}
			now := time.Now()
			for _, s := range states {
				if isSessionActive(s, now) || !hasUnconsumedBytes(s) {
					continue
				}
				if spawn(s) {
					atelierlog.Info("sessions-manager: dormant session revived", "session", s.ClaudeSessionID)
				}
			}
		}
	}
}

// runSessionReader reads one session tree every treePollInterval until ctx is
// cancelled, the state disappears, or the whole tree has been quiet for
// sessionIdleTimeout; the manager's spawn filter then keeps the session
// dormant until new bytes appear.
func runSessionReader(ctx context.Context, claudeSessionID string) {
	atelierlog.Info("session-reader: started", "session", claudeSessionID)

	tick := time.NewTicker(treePollInterval)
	defer tick.Stop()
	for {
		state, err := readSessionTree(ctx, claudeSessionID)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				atelierlog.Error("session-reader: load state failed; stopping", "session", claudeSessionID, "err", err.Error())
			}
			return
		}
		if idle := time.Since(state.LastActivityAt); idle >= sessionIdleTimeout {
			atelierlog.Info("session-reader: idle-exit", "session", claudeSessionID, "idle", idle.String())
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// readSessionTree consumes every complete line newly available in the tree,
// writes the derived envelopes to the outbox, then saves the state once.
// Envelopes go first: a crash in between re-reads the lines, and the
// re-derived heartbeats collapse on their queued file and, once shipped, on
// their Firestore doc (409). The state is reloaded on every poll so a
// session-start re-registration made by `atelierd emit` is never overwritten
// by a stale in-memory copy.
func readSessionTree(ctx context.Context, claudeSessionID string) (*transcript.State, error) {
	state, err := transcript.LoadState(claudeSessionID)
	if err != nil {
		return nil, err
	}

	var envs []*outbox.Envelope
	advanced := false
	for _, f := range state.TreeFiles() {
		if ctx.Err() != nil {
			break
		}
		fileEnvs, moved := consumeTranscript(state, f)
		envs = append(envs, fileEnvs...)
		advanced = advanced || moved
	}
	if !advanced {
		return state, nil
	}

	if readHook != nil {
		readHook()
	}
	now := time.Now().UTC()
	for _, env := range envs {
		if env.TS != nil && env.TS.After(now.Add(maxHeartbeatSkew)) {
			state.ForgetMinute(*env.TS)
			atelierlog.Warn("session-reader: heartbeat ahead of the local clock, dropped", "session", claudeSessionID, "minute", env.TS.Format(time.RFC3339))
			continue
		}
		if werr := outbox.Write(env); werr != nil {
			atelierlog.Warn("session-reader: outbox write failed; will re-read", "session", claudeSessionID, "type", env.Type, "err", werr.Error())
			return state, nil
		}
	}

	state.LastActivityAt = now
	if current, lerr := transcript.LoadState(claudeSessionID); lerr == nil && current.JSONLPath != state.JSONLPath {
		// `atelierd emit` moved the session to another transcript during
		// this read: keep its registration, whose offsets belong to the new
		// file, and only add the minutes this read emitted.
		for minute := range state.EmittedMinutes {
			current.EmittedMinutes[minute] = true
		}
		current.LastActivityAt = now
		state = current
	}
	if serr := transcript.SaveState(state); serr != nil {
		atelierlog.Warn("session-reader: save state failed", "session", claudeSessionID, "err", serr.Error())
	}
	return state, nil
}

// consumeTranscript derives every complete line of path past its offset and
// advances the offset in state. A trailing line without its newline waits for
// the next poll. Truncation (size below the offset) restarts the file at 0.
func consumeTranscript(state *transcript.State, path string) ([]*outbox.Envelope, bool) {
	f, err := os.Open(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			atelierlog.Warn("session-reader: open transcript failed", "session", state.ClaudeSessionID, "path", path, "err", err.Error())
		}
		return nil, false
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil, false
	}
	stored := state.Offset(path)
	if stat.Size() == stored {
		return nil, false
	}
	offset := stored
	if stat.Size() < offset {
		atelierlog.Warn("session-reader: transcript truncated; restarting from offset 0", "session", state.ClaudeSessionID, "path", path, "size", stat.Size(), "offset", offset)
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		atelierlog.Warn("session-reader: seek failed", "session", state.ClaudeSessionID, "path", path, "err", err.Error())
		return nil, false
	}

	var envs []*outbox.Envelope
	r := bufio.NewReaderSize(f, 64*1024)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			break
		}
		offset += int64(len(line))
		envs = append(envs, transcript.Derive(state, line, nil, ulid.New)...)
	}
	if offset == stored {
		return envs, false
	}
	state.SetOffset(path, offset)
	return envs, true
}

// stateGCLoop purges session states whose transcript no longer exists (Claude
// Code deletes transcripts after ~30 days) so ~/.atelier/sessions/ stops
// growing without bound. Startup run + daily ticker, same shape as
// updaterLoop.
func stateGCLoop(ctx context.Context, _ *runState) {
	runStateGC()
	tick := time.NewTicker(stateGCInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			runStateGC()
		}
	}
}

// runStateGC deletes a state only when its parent transcript stat returns
// ErrNotExist — any other stat error is indistinguishable from a transient
// mount/permission hiccup and must not destroy the offsets that guard against
// re-emission. An active state is never deleted: its transcript may be
// transiently absent mid-rotation.
func runStateGC() {
	states, err := transcript.ListStates()
	if err != nil {
		atelierlog.Warn("state-gc: list states failed", "err", err.Error())
		return
	}
	now := time.Now()
	deleted := 0
	for _, s := range states {
		if isSessionActive(s, now) {
			continue
		}
		if _, serr := os.Stat(s.JSONLPath); !errors.Is(serr, os.ErrNotExist) {
			continue
		}
		if derr := transcript.DeleteState(s.ClaudeSessionID); derr != nil {
			atelierlog.Warn("state-gc: delete failed", "session", s.ClaudeSessionID, "err", derr.Error())
			continue
		}
		deleted++
	}
	legacy, err := transcript.PruneOrphanLegacyDirs()
	if err != nil {
		atelierlog.Warn("state-gc: prune legacy subagent states failed", "err", err.Error())
	}
	atelierlog.Info("state-gc: removed orphan states", "scanned", len(states), "deleted", deleted, "legacyDirs", legacy)
}
