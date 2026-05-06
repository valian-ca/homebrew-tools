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
	// sessionPollInterval is the safety net for fsnotify. fsnotify on macOS
	// occasionally drops events under load; a periodic re-scan catches up
	// (same pattern as the shipper's reconcileInterval).
	sessionPollInterval = 5 * time.Second
)

// sessionsManagerLoop is the sixth atelierd run goroutine. Its job is to keep
// one watcher per known session alive — discovering new sessions written to
// ~/.atelier/sessions/ by `atelierd emit hook:session-start --data jsonlPath`,
// and respawning watchers on daemon startup for sessions that survived a
// previous run.
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

	spawn := func(state *transcript.State) {
		mu.Lock()
		if _, exists := watchers[state.ClaudeSessionID]; exists {
			mu.Unlock()
			return
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
	}

	// Initial scan: rehydrate every session that was on disk when atelierd run
	// started. Sessions registered while the daemon was down are picked up
	// here so AC 4 (kill -9 + 30 s downtime) works without losing sessions.
	states, err := transcript.ListStates()
	if err != nil {
		atelierlog.Warn("sessions-manager: initial scan failed", "err", err.Error())
	}
	for _, s := range states {
		spawn(s)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		atelierlog.Error("sessions-manager: fsnotify init failed; relying on poll only", "err", err.Error())
	} else {
		defer watcher.Close()
		if werr := watcher.Add(transcript.SessionsDir()); werr != nil {
			atelierlog.Error("sessions-manager: fsnotify add failed", "err", werr.Error())
			watcher = nil
		}
	}

	tick := time.NewTicker(sessionPollInterval)
	defer tick.Stop()

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
			states, err := transcript.ListStates()
			if err != nil {
				continue
			}
			for _, s := range states {
				spawn(s)
			}
		case ev, ok := <-watcherEventsRaw(watcher):
			if !ok {
				continue
			}
			if !shouldHandleSessionEvent(ev) {
				continue
			}
			id := sessionIDFromEventName(ev.Name)
			if id == "" {
				continue
			}
			s, lerr := transcript.LoadState(id)
			if lerr != nil {
				atelierlog.Warn("sessions-manager: load state failed", "session", id, "err", lerr.Error())
				continue
			}
			spawn(s)
		}
	}
}

func watcherEventsRaw(w *fsnotify.Watcher) <-chan fsnotify.Event {
	if w == nil {
		return nil
	}
	return w.Events
}

func shouldHandleSessionEvent(ev fsnotify.Event) bool {
	if ev.Op&(fsnotify.Create|fsnotify.Write) == 0 {
		return false
	}
	if !strings.HasSuffix(ev.Name, ".json") {
		return false
	}
	if strings.HasSuffix(ev.Name, ".tmp") {
		return false
	}
	return true
}

func sessionIDFromEventName(name string) string {
	base := filepath.Base(name)
	if !strings.HasSuffix(base, ".json") {
		return ""
	}
	return strings.TrimSuffix(base, ".json")
}

// runSessionWatcher watches a single Claude Code transcript JSONL file and
// derives events from each line as they're appended. The function returns
// only when ctx is cancelled (daemon shutdown) — there is no idle exit;
// hook:session-end is emitted by the bash hook, not by atelierd.
//
// Crash-safety: state is saved to disk BEFORE writing envelopes to the
// outbox. This favors zero-duplication over zero-loss (AC 4 says "sans
// duplication"; loss in the small mid-batch crash window is tolerated). On
// restart, transcript.Derive's in-state dedup (LastMsgID, ClosedToolUseIDs,
// LastPromptID, OpenToolUseTools) suppresses re-emission of any line whose
// state was already persisted.
func runSessionWatcher(ctx context.Context, claudeSessionID string) {
	state, err := transcript.LoadState(claudeSessionID)
	if err != nil {
		atelierlog.Error("session-watcher: load state failed; aborting", "session", claudeSessionID, "err", err.Error())
		return
	}

	atelierlog.Info("session-watcher: started", "session", claudeSessionID, "jsonl", state.JSONLPath, "offset", state.Offset)

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

	// First catch-up read so any lines appended while atelierd was down (AC 4)
	// are processed before we start blocking on fsnotify.
	state = consume(ctx, state)

	tick := time.NewTicker(sessionPollInterval)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			state = consume(ctx, state)
		case ev, ok := <-watcherEventsRaw(watcher):
			if !ok {
				continue
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
				continue
			}
			state = consume(ctx, state)
		}
	}
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
			atelierlog.Warn("session-watcher: open jsonl failed", "session", state.ClaudeSessionID, "err", err.Error())
		}
		return state
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return state
	}
	if stat.Size() < state.Offset {
		atelierlog.Warn("session-watcher: jsonl truncated; restarting from offset 0", "session", state.ClaudeSessionID, "size", stat.Size(), "offset", state.Offset)
		state.Offset = 0
	}

	if _, err := f.Seek(state.Offset, io.SeekStart); err != nil {
		atelierlog.Warn("session-watcher: seek failed", "session", state.ClaudeSessionID, "err", err.Error())
		return state
	}

	bytesRead, err := io.ReadAll(f)
	if err != nil {
		atelierlog.Warn("session-watcher: read failed", "session", state.ClaudeSessionID, "err", err.Error())
		return state
	}
	if len(bytesRead) == 0 {
		return state
	}

	// Process complete lines only — a trailing partial line (no \n yet) waits
	// for the next append.
	consumed := int64(0)
	for {
		idx := bytes.IndexByte(bytesRead[consumed:], '\n')
		if idx < 0 {
			break
		}
		line := bytesRead[consumed : consumed+int64(idx)]
		envelopes, derr := transcript.Derive(state, line, nil, ulid.New)
		if derr != nil {
			atelierlog.Warn("session-watcher: derive error (skipping line)", "session", state.ClaudeSessionID, "err", derr.Error())
		}
		consumed += int64(idx) + 1 // past the \n
		state.Offset += int64(idx) + 1
		state.LastActivityAt = time.Now().UTC()

		// Save state BEFORE writing envelopes to the outbox. On a kill -9 in
		// the brief window between SaveState and outbox.Write the events
		// won't ship, but on restart the line will be skipped (offset has
		// advanced) and we won't re-emit them either — zero duplication, at
		// most a tiny lost-window. AC 4 explicitly forbids duplication; loss
		// in this micro-window is acceptable.
		if serr := transcript.SaveState(state); serr != nil {
			atelierlog.Warn("session-watcher: save state failed", "session", state.ClaudeSessionID, "err", serr.Error())
			return state
		}

		for _, env := range envelopes {
			if werr := outbox.Write(env); werr != nil {
				atelierlog.Warn("session-watcher: outbox write failed", "session", state.ClaudeSessionID, "type", env.Type, "err", werr.Error())
			}
		}
	}

	return state
}
