package cmds

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	atelierlog "github.com/valian-ca/homebrew-tools/internal/atelierd/log"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/outbox"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/paths"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/transcript"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/ulid"
)

const (
	// subagentFilePrefix matches the names Claude Code writes under
	// <parentSessionId>/subagents/. The full pattern is "agent-<id>.jsonl".
	subagentFilePrefix = "agent-"

	// subagentDirName is the directory Claude Code lazily creates next to a
	// parent JSONL the first time the parent invokes a Task subagent.
	subagentDirName = "subagents"
)

// sessionPollInterval is the safety net for fsnotify. fsnotify on macOS
// occasionally drops events under load; a periodic re-scan catches up
// (same pattern as the shipper's reconcileInterval). Var rather than const
// so integration tests can drive the watchers with a sub-second tick.
var sessionPollInterval = 5 * time.Second

// subagentPreAttachInterval is the slower poll a parent's subagentDirManager
// runs before the subagents/ directory exists. Most sessions never invoke a
// subagent, so the steady-state cost of a 5 s os.Stat per parent is wasted —
// 30 s is still snappy enough that the first subagent invocation surfaces
// quickly, and the manager flips back to sessionPollInterval on attach.
var subagentPreAttachInterval = 30 * time.Second

// sessionIdleTimeout bounds watching to active sessions: a session tree with
// no consumed line for this long releases its watchers, and dormant states
// on disk don't get one at startup. Var so tests can shrink it.
var sessionIdleTimeout = 30 * time.Minute

// dormantScanInterval paces the revival check of dormant sessions — one
// os.Stat of the parent JSONL per dormant state per scan, instead of an
// open+read every 5 s. 30 s keeps the resume-latency promise (≤ 30 s) at
// ~2 % of the old per-session cost.
var dormantScanInterval = 30 * time.Second

// stateGCInterval paces the orphan-state purge after the startup run. Claude
// Code deletes transcripts after ~30 days, so daily is more than enough.
var stateGCInterval = 24 * time.Hour

func isSessionActive(s *transcript.State, now time.Time) bool {
	return now.Sub(s.LastActivityAt) < sessionIdleTimeout
}

// hasUnconsumedBytes reports whether the JSONL holds bytes the offset hasn't
// consumed. Size below the offset (truncation) counts too — consume resets
// to 0 and re-reads. A missing or unreadable JSONL is "nothing to consume".
func hasUnconsumedBytes(s *transcript.State) bool {
	stat, err := os.Stat(s.JSONLPath)
	if err != nil {
		return false
	}
	return stat.Size() != s.Offset
}

func shouldSpawnWatcher(s *transcript.State, now time.Time) bool {
	return isSessionActive(s, now) || hasUnconsumedBytes(s)
}

// activityTracker is the shared last-progress clock of one session tree: the
// parent watcher and every subagent watcher touch it on each consumed batch,
// and the parent idle-exits only when the whole tree has been quiet — a
// parent must not tear down while its subagents still stream.
type activityTracker struct {
	mu   sync.Mutex
	last time.Time
}

func newActivityTracker() *activityTracker {
	return &activityTracker{last: time.Now()}
}

func (a *activityTracker) touch() {
	a.mu.Lock()
	a.last = time.Now()
	a.mu.Unlock()
}

func (a *activityTracker) idleFor(now time.Time) time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	return now.Sub(a.last)
}

// Subagent transcripts are not spawned here: each parent's runSessionWatcher
// owns a sibling subagentDirManager that watches its own
// <parentJsonl-without-ext>/subagents/ directory.
func sessionsManagerLoop(ctx context.Context, _ *runState) {
	if err := paths.EnsureDir(transcript.SessionsDir()); err != nil {
		atelierlog.Error("sessions-manager: ensure sessions dir failed", "err", err.Error())
		<-ctx.Done()
		return
	}

	type live struct {
		cancel context.CancelFunc
	}
	watchers := map[string]live{}
	var wg sync.WaitGroup
	var mu sync.Mutex

	spawn := func(state *transcript.State) bool {
		if state.IsSubagent() {
			return false
		}
		mu.Lock()
		if _, exists := watchers[state.ClaudeSessionID]; exists {
			mu.Unlock()
			return false
		}
		wctx, cancel := context.WithCancel(ctx)
		watchers[state.ClaudeSessionID] = live{cancel: cancel}
		mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				mu.Lock()
				delete(watchers, state.ClaudeSessionID)
				mu.Unlock()
			}()
			runSessionWatcher(wctx, state.ClaudeSessionID)
		}()
		return true
	}

	// Initial scan: rehydrate only the parent sessions worth watching —
	// active ones, plus dormant ones whose JSONL grew while the daemon was
	// down. The rest stay on disk for the dormant scan. Subagent
	// state files are inspected only to flag orphans — a parent's
	// subagentDirManager will rediscover its live subagents from
	// <jsonl>/subagents/ and resume from the persisted offset.
	states, err := transcript.ListStates()
	if err != nil {
		atelierlog.Warn("sessions-manager: initial scan failed", "err", err.Error())
	}
	now := time.Now()
	parentIDs := map[string]bool{}
	for _, s := range states {
		if !s.IsSubagent() {
			parentIDs[s.ClaudeSessionID] = true
		}
	}
	for _, s := range states {
		if !s.IsSubagent() {
			if shouldSpawnWatcher(s, now) {
				spawn(s)
			}
			continue
		}
		if !parentIDs[s.ParentSessionID()] {
			atelierlog.Warn("sessions-manager: orphan subagent state, parent not registered",
				"watcherKey", s.WatcherKey)
		}
	}

	// No fsnotify on SessionsDir: watching a directory on kqueue opens one fd
	// per file inside it, so the watch alone would pin as many descriptors as
	// there are state files on disk (the 30-day session backlog). The 5 s
	// poll below is the discovery path; its worst case is exactly the ≤ 5 s
	// bound promised for fresh sessions.
	tick := time.NewTicker(sessionPollInterval)
	defer tick.Stop()
	dormantTick := time.NewTicker(dormantScanInterval)
	defer dormantTick.Stop()

	for {
		select {
		case <-ctx.Done():
			mu.Lock()
			for _, l := range watchers {
				l.cancel()
			}
			mu.Unlock()
			wg.Wait()
			return
		case <-tick.C:
			// Active states only: stat-ing every dormant JSONL here would put
			// the whole fleet back on a 5 s cadence — dormants belong to the
			// slower dormant scan.
			states, err := transcript.ListStates()
			if err != nil {
				continue
			}
			now := time.Now()
			for _, s := range states {
				if s.IsSubagent() || !isSessionActive(s, now) {
					continue
				}
				spawn(s)
			}
		case <-dormantTick.C:
			states, err := transcript.ListStates()
			if err != nil {
				continue
			}
			now := time.Now()
			for _, s := range states {
				if s.IsSubagent() || isSessionActive(s, now) || !hasUnconsumedBytes(s) {
					continue
				}
				if spawn(s) {
					atelierlog.Info("sessions-manager: dormant session revived", "session", s.ClaudeSessionID)
				}
			}
		}
	}
}

func watcherEventsRaw(w *fsnotify.Watcher) <-chan fsnotify.Event {
	if w == nil {
		return nil
	}
	return w.Events
}

// The function returns when ctx is cancelled or when the whole session tree
// (this parent plus its subagent watchers) has been idle ≥ sessionIdleTimeout;
// hook:session-end is emitted by the bash hook, not by atelierd.
// The idle-exit return path runs the same defers as cancellation — fsnotify
// watcher closed, subagent manager cancelled — and the manager's spawn filter
// keeps the session dormant until new bytes appear.
//
// Crash-safety: state is saved to disk BEFORE writing envelopes to the
// outbox. This favors zero-duplication over zero-loss; loss in the small
// mid-batch crash window is tolerated. On restart, transcript.Derive's
// in-state dedup (LastMsgID, ClosedToolUseIDs, LastPromptID,
// OpenToolUseTools) suppresses re-emission of any line whose state was
// already persisted.
func runSessionWatcher(ctx context.Context, claudeSessionID string) {
	state, err := transcript.LoadState(claudeSessionID)
	if err != nil {
		atelierlog.Error("session-watcher: load state failed; aborting", "session", claudeSessionID, "err", err.Error())
		return
	}

	atelierlog.Info("session-watcher: started", "session", claudeSessionID, "jsonl", state.JSONLPath, "offset", state.Offset)

	tree := newActivityTracker()

	// Copied before the goroutine launch: the watcher loop reassigns state on
	// every consume, and the manager goroutine must not read through it.
	jsonlPath := state.JSONLPath

	subCtx, subCancel := context.WithCancel(ctx)
	var subWG sync.WaitGroup
	subWG.Add(1)
	go func() {
		defer subWG.Done()
		subagentDirManager(subCtx, claudeSessionID, jsonlPath, tree)
	}()
	defer func() {
		subCancel()
		subWG.Wait()
	}()

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		atelierlog.Warn("session-watcher: fsnotify init failed; polling only", "err", err.Error())
	} else {
		defer watcher.Close()
		if werr := watcher.Add(state.JSONLPath); werr != nil {
			atelierlog.Warn("session-watcher: fsnotify add failed; polling only", "session", claudeSessionID, "err", werr.Error())
			watcher = nil
		}
	}

	consumeTracked := func() {
		before := state.Offset
		state = consume(ctx, state)
		if state.Offset != before {
			tree.touch()
		}
	}

	consumeTracked()

	tick := time.NewTicker(sessionPollInterval)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			consumeTracked()
			if idle := tree.idleFor(time.Now()); idle >= sessionIdleTimeout {
				atelierlog.Info("session-watcher: idle-exit", "session", claudeSessionID, "idle", idle.String())
				return
			}
		case ev, ok := <-watcherEventsRaw(watcher):
			if !ok {
				watcher = nil
				continue
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
				continue
			}
			consumeTracked()
		}
	}
}

// The <parentJsonl-without-ext>/subagents/ directory is created lazily by
// Claude Code on the first Task invocation; until then this loop polls
// silently — no log output for sessions that never invoke a subagent.
//
// Cardinality is hybrid by necessity: fsnotify on darwin (kqueue) only wakes
// on dir-entry mutations when watching a directory, never on writes to
// files inside it. So this manager owns the dir-watch (CREATE / RENAME),
// while each runSubagentWatcher owns its own file-watch for appends —
// exactly mirroring sessionsManagerLoop / runSessionWatcher at the parent
// layer.
func subagentDirManager(ctx context.Context, parentSessionID, parentJSONLPath string, tree *activityTracker) {
	if !strings.HasSuffix(parentJSONLPath, ".jsonl") {
		atelierlog.Warn("subagent-manager: parent jsonl path lacks .jsonl suffix; subagent watching disabled",
			"parent", parentSessionID, "path", parentJSONLPath)
		<-ctx.Done()
		return
	}
	subagentDir := filepath.Join(strings.TrimSuffix(parentJSONLPath, ".jsonl"), subagentDirName)

	type live struct {
		cancel context.CancelFunc
	}
	spawned := map[string]live{}
	var wg sync.WaitGroup
	var mu sync.Mutex

	// dormantOffsets caches the offset of subagents skipped as dormant, so
	// the 5 s re-scan costs one os.Stat per dormant file instead of a full
	// state read + unmarshal. Only the manager goroutine touches it.
	dormantOffsets := map[string]int64{}

	spawn := func(jsonlPath string) {
		base := filepath.Base(jsonlPath)
		if !strings.HasPrefix(base, subagentFilePrefix) || !strings.HasSuffix(base, ".jsonl") {
			return
		}
		agentBase := strings.TrimSuffix(base, ".jsonl")
		watcherKey := transcript.SubagentWatcherKey(parentSessionID, agentBase)

		if off, ok := dormantOffsets[watcherKey]; ok {
			st, serr := os.Stat(jsonlPath)
			if serr != nil || st.Size() == off {
				return
			}
			delete(dormantOffsets, watcherKey)
		}

		mu.Lock()
		if _, exists := spawned[watcherKey]; exists {
			mu.Unlock()
			return
		}
		wctx, cancel := context.WithCancel(ctx)
		spawned[watcherKey] = live{cancel: cancel}
		mu.Unlock()

		existing, err := transcript.LoadState(watcherKey)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				atelierlog.Warn("subagent-manager: load state failed", "parent", parentSessionID, "agent", agentBase, "err", err.Error())
				mu.Lock()
				delete(spawned, watcherKey)
				mu.Unlock()
				cancel()
				return
			}
			fresh := &transcript.State{
				ClaudeSessionID: parentSessionID,
				WatcherKey:      watcherKey,
				JSONLPath:       jsonlPath,
				LastActivityAt:  time.Now().UTC(),
			}
			if serr := transcript.SaveState(fresh); serr != nil {
				atelierlog.Warn("subagent-manager: persist initial state failed", "parent", parentSessionID, "agent", agentBase, "err", serr.Error())
				mu.Lock()
				delete(spawned, watcherKey)
				mu.Unlock()
				cancel()
				return
			}
		} else if !shouldSpawnWatcher(existing, time.Now()) {
			dormantOffsets[watcherKey] = existing.Offset
			mu.Lock()
			delete(spawned, watcherKey)
			mu.Unlock()
			cancel()
			return
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				mu.Lock()
				delete(spawned, watcherKey)
				mu.Unlock()
			}()
			runSubagentWatcher(wctx, watcherKey, tree)
		}()
	}

	scan := func() {
		entries, err := os.ReadDir(subagentDir)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				atelierlog.Warn("subagent-manager: read dir failed", "parent", parentSessionID, "err", err.Error())
			}
			return
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			n := e.Name()
			if !strings.HasPrefix(n, subagentFilePrefix) || !strings.HasSuffix(n, ".jsonl") {
				continue
			}
			spawn(filepath.Join(subagentDir, n))
		}
	}

	var watcher *fsnotify.Watcher
	var dirReady bool

	tryAttach := func() {
		if watcher != nil {
			return
		}
		if _, err := os.Stat(subagentDir); err != nil {
			return
		}
		if !dirReady {
			dirReady = true
			atelierlog.Info("subagent-manager: subagents dir detected", "parent", parentSessionID, "dir", subagentDir)
		}
		w, werr := fsnotify.NewWatcher()
		if werr != nil {
			atelierlog.Warn("subagent-manager: fsnotify init failed; will retry next tick", "parent", parentSessionID, "err", werr.Error())
			return
		}
		if aerr := w.Add(subagentDir); aerr != nil {
			atelierlog.Warn("subagent-manager: fsnotify add failed; will retry next tick", "parent", parentSessionID, "err", aerr.Error())
			_ = w.Close()
			return
		}
		watcher = w
	}

	tick := time.NewTicker(subagentPreAttachInterval)
	defer tick.Stop()
	defer func() {
		mu.Lock()
		for _, l := range spawned {
			l.cancel()
		}
		mu.Unlock()
		wg.Wait()
		if watcher != nil {
			_ = watcher.Close()
		}
	}()

	tryAttach()
	if dirReady {
		tick.Reset(sessionPollInterval)
		scan()
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			wasReady := dirReady
			tryAttach()
			if !wasReady && dirReady {
				tick.Reset(sessionPollInterval)
			}
			scan()
		case ev, ok := <-watcherEventsRaw(watcher):
			if !ok {
				watcher = nil
				continue
			}
			if !shouldHandleSubagentEvent(ev) {
				continue
			}
			spawn(ev.Name)
		}
	}
}

func shouldHandleSubagentEvent(ev fsnotify.Event) bool {
	if ev.Op&(fsnotify.Create|fsnotify.Rename|fsnotify.Write) == 0 {
		return false
	}
	base := filepath.Base(ev.Name)
	if !strings.HasPrefix(base, subagentFilePrefix) {
		return false
	}
	if !strings.HasSuffix(base, ".jsonl") {
		return false
	}
	return true
}

// runSubagentWatcher idle-exits on its own clock — a finished subagent frees
// its kqueue while the parent lives — but every consumed batch also touches
// the shared tree tracker so an active subagent keeps its parent alive.
func runSubagentWatcher(ctx context.Context, watcherKey string, tree *activityTracker) {
	state, err := transcript.LoadState(watcherKey)
	if err != nil {
		atelierlog.Error("subagent-watcher: load state failed; aborting", "watcherKey", watcherKey, "err", err.Error())
		return
	}

	atelierlog.Info("subagent-watcher: started", "parent", state.ClaudeSessionID, "watcherKey", watcherKey, "jsonl", state.JSONLPath, "offset", state.Offset)

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		atelierlog.Warn("subagent-watcher: fsnotify init failed; polling only", "watcherKey", watcherKey, "err", err.Error())
	} else {
		defer watcher.Close()
		if werr := watcher.Add(state.JSONLPath); werr != nil {
			atelierlog.Warn("subagent-watcher: fsnotify add failed; polling only", "watcherKey", watcherKey, "err", werr.Error())
			watcher = nil
		}
	}

	lastOwn := time.Now()
	consumeTracked := func() {
		before := state.Offset
		state = consume(ctx, state)
		if state.Offset != before {
			lastOwn = time.Now()
			tree.touch()
		}
	}

	consumeTracked()

	tick := time.NewTicker(sessionPollInterval)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			consumeTracked()
			if idle := time.Since(lastOwn); idle >= sessionIdleTimeout {
				atelierlog.Info("subagent-watcher: idle-exit", "watcherKey", watcherKey, "idle", idle.String())
				return
			}
		case ev, ok := <-watcherEventsRaw(watcher):
			if !ok {
				watcher = nil
				continue
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
				continue
			}
			consumeTracked()
		}
	}
}

// stateGCLoop purges session states whose transcript JSONL no longer exists
// (Claude Code deletes transcripts after ~30 days) so ~/.atelier/sessions/
// stops growing without bound. Startup run + daily ticker, same shape as
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

// runStateGC deletes a state only when its JSONL stat returns ErrNotExist —
// any other stat error is indistinguishable from a transient mount/permission
// hiccup and must not destroy the offset that guards zero-duplication. Two
// more guards protect the same contract: an active state is never deleted
// (its JSONL may be transiently absent mid-rotation), and subagent states
// whose parent state is gone are purged with it — without a registered
// parent no manager will ever watch them again.
func runStateGC() {
	states, err := transcript.ListStates()
	if err != nil {
		atelierlog.Warn("state-gc: list states failed", "err", err.Error())
		return
	}
	now := time.Now()
	deleted := 0
	remove := func(s *transcript.State) {
		if derr := transcript.DeleteState(s.Key()); derr != nil {
			atelierlog.Warn("state-gc: delete failed", "key", s.Key(), "err", derr.Error())
			return
		}
		deleted++
	}
	liveParents := map[string]bool{}
	for _, s := range states {
		if s.IsSubagent() {
			continue
		}
		if !isSessionActive(s, now) {
			if _, serr := os.Stat(s.JSONLPath); errors.Is(serr, os.ErrNotExist) {
				remove(s)
				continue
			}
		}
		liveParents[s.ClaudeSessionID] = true
	}
	for _, s := range states {
		if !s.IsSubagent() || isSessionActive(s, now) {
			continue
		}
		if !liveParents[s.ParentSessionID()] {
			remove(s)
			continue
		}
		if _, serr := os.Stat(s.JSONLPath); errors.Is(serr, os.ErrNotExist) {
			remove(s)
		}
	}
	atelierlog.Info("state-gc: removed orphan states", "scanned", len(states), "deleted", deleted)
}

// consume reads every complete line newly available past state.Offset, derives
// events for each, persists the new state, then writes the envelopes to the
// outbox. Returns the updated state. Truncation (file shrunk below the offset)
// is handled by resetting offset to 0 and starting over — defensive, the
// scenario is unusual.
func consume(ctx context.Context, state *transcript.State) *transcript.State {
	if ctx.Err() != nil {
		return state
	}
	f, err := os.Open(state.JSONLPath)
	if err != nil {
		// File may be transiently absent (rotation, deletion). Keep the
		// session record and retry on next tick / fsnotify event.
		if !errors.Is(err, os.ErrNotExist) {
			atelierlog.Warn("session-watcher: open jsonl failed", "watcherKey", state.Key(), "session", state.ClaudeSessionID, "err", err.Error())
		}
		return state
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return state
	}
	if stat.Size() < state.Offset {
		atelierlog.Warn("session-watcher: jsonl truncated; restarting from offset 0", "watcherKey", state.Key(), "session", state.ClaudeSessionID, "size", stat.Size(), "offset", state.Offset)
		state.Offset = 0
	}

	if _, err := f.Seek(state.Offset, io.SeekStart); err != nil {
		atelierlog.Warn("session-watcher: seek failed", "watcherKey", state.Key(), "session", state.ClaudeSessionID, "err", err.Error())
		return state
	}

	bytesRead, err := io.ReadAll(f)
	if err != nil {
		atelierlog.Warn("session-watcher: read failed", "watcherKey", state.Key(), "session", state.ClaudeSessionID, "err", err.Error())
		return state
	}
	if len(bytesRead) == 0 {
		return state
	}

	consumed := int64(0)
	for {
		newlineIdx := bytes.IndexByte(bytesRead[consumed:], '\n')
		if newlineIdx < 0 {
			break
		}
		line := bytesRead[consumed : consumed+int64(newlineIdx)]
		envelopes, derr := transcript.Derive(state, line, nil, ulid.New)
		if derr != nil {
			atelierlog.Warn("session-watcher: derive error (skipping line)", "watcherKey", state.Key(), "session", state.ClaudeSessionID, "err", derr.Error())
		}
		consumed += int64(newlineIdx) + 1
		state.Offset += int64(newlineIdx) + 1
		state.LastActivityAt = time.Now().UTC()

		// Save state BEFORE writing envelopes to the outbox. On a kill -9 in
		// the brief window between SaveState and outbox.Write the events
		// won't ship, but on restart the line will be skipped (offset has
		// advanced) and we won't re-emit them either — zero duplication, at
		// most a tiny lost-window. The contract forbids duplication; loss in
		// this micro-window is acceptable.
		if serr := transcript.SaveState(state); serr != nil {
			atelierlog.Warn("session-watcher: save state failed", "watcherKey", state.Key(), "session", state.ClaudeSessionID, "err", serr.Error())
			return state
		}

		for _, env := range envelopes {
			if werr := outbox.Write(env); werr != nil {
				atelierlog.Warn("session-watcher: outbox write failed", "watcherKey", state.Key(), "session", state.ClaudeSessionID, "type", env.Type, "err", werr.Error())
			}
		}
	}

	return state
}
