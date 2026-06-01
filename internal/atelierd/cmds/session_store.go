package cmds

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	atelierlog "github.com/valian-ca/homebrew-tools/internal/atelierd/log"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/outbox"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/paths"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/sessionstore"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/ulid"
)

// sessionStoreWatcherLoop tails the Claude Desktop session store and emits
// title events for sessions Desktop never writes into the transcript. The 5s
// poll is the workhorse — it satisfies the "within seconds" guarantee on its
// own; fsnotify only shortens the latency when it fires. The store nests titles
// two directories deep and Desktop creates session subdirs on the fly, so
// watches are re-added on every tick rather than once at startup.
func sessionStoreWatcherLoop(ctx context.Context, _ *runState) {
	if err := paths.EnsureDir(paths.SessionTitles()); err != nil {
		atelierlog.Error("session-store-watcher: ensure state dir failed", "err", err.Error())
		<-ctx.Done()
		return
	}
	storeRoot, err := paths.ClaudeDesktopSessionStore()
	if err != nil {
		atelierlog.Error("session-store-watcher: resolve desktop store failed", "err", err.Error())
		<-ctx.Done()
		return
	}

	watcher, werr := fsnotify.NewWatcher()
	if werr != nil {
		atelierlog.Error("session-store-watcher: fsnotify init failed; relying on poll only", "err", werr.Error())
		watcher = nil
	} else {
		defer watcher.Close()
		addStoreWatches(watcher, storeRoot)
	}

	reconcileSessionStore(storeRoot)

	tick := time.NewTicker(sessionPollInterval)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			addStoreWatches(watcher, storeRoot)
			reconcileSessionStore(storeRoot)
		case ev, ok := <-watcherEventsRaw(watcher):
			if !ok {
				watcher = nil
				continue
			}
			if !shouldHandleStoreEvent(ev) {
				continue
			}
			drainEvents(watcher.Events, 200*time.Millisecond)
			if ev.Op&fsnotify.Create != 0 {
				addStoreWatches(watcher, storeRoot)
			}
			reconcileSessionStore(storeRoot)
		}
	}
}

// addStoreWatches attaches a watch to root and every subdirectory under it.
// fsnotify on darwin (kqueue) does not recurse, and adding an already-watched
// path is a no-op, so a full re-walk each tick is the simplest way to pick up
// session subdirs Desktop creates after startup. Best-effort throughout — the
// poll reconcile is the safety net.
func addStoreWatches(w *fsnotify.Watcher, root string) {
	if w == nil {
		return
	}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			_ = w.Add(path)
		}
		return nil
	})
}

func shouldHandleStoreEvent(ev fsnotify.Event) bool {
	if ev.Op&(fsnotify.Create|fsnotify.Write) == 0 {
		return false
	}
	base := filepath.Base(ev.Name)
	if strings.HasSuffix(base, ".tmp") {
		return false
	}
	if strings.HasSuffix(base, ".json") {
		return strings.HasPrefix(base, "local_")
	}
	// A directory create is a new session subdir — let it through so the next
	// addStoreWatches attaches to it.
	return ev.Op&fsnotify.Create != 0
}

func reconcileSessionStore(storeRoot string) {
	entries, err := sessionstore.ScanEntries(storeRoot)
	if err != nil {
		atelierlog.Warn("session-store-watcher: scan failed", "err", err.Error())
		return
	}
	for _, entry := range entries {
		consumeStoreEntry(entry)
	}
}

// consumeStoreEntry derives the title event for one store entry and, when there
// is one to emit, persists the new state BEFORE writing the envelope — the same
// crash-safety order as the transcript watcher's consume(): a kill -9 in the
// gap drops the event rather than risking a duplicate on restart.
func consumeStoreEntry(entry sessionstore.Entry) {
	state, err := sessionstore.LoadState(entry.CliSessionID)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			atelierlog.Warn("session-store-watcher: load state failed", "session", entry.CliSessionID, "err", err.Error())
			return
		}
		state = nil
	}

	envelopes, derr := sessionstore.Derive(state, entry, nil, ulid.NewAt)
	if derr != nil {
		atelierlog.Warn("session-store-watcher: derive failed", "session", entry.CliSessionID, "err", derr.Error())
		return
	}
	if len(envelopes) == 0 {
		return
	}

	next := &sessionstore.State{
		CliSessionID:    entry.CliSessionID,
		LastTitle:       entry.Title,
		LastTitleSource: entry.TitleSource,
		LastActivityAt:  time.Now().UTC(),
	}
	if serr := sessionstore.SaveState(next); serr != nil {
		atelierlog.Warn("session-store-watcher: save state failed", "session", entry.CliSessionID, "err", serr.Error())
		return
	}

	for _, env := range envelopes {
		if werr := outbox.Write(env); werr != nil {
			atelierlog.Warn("session-store-watcher: outbox write failed", "session", entry.CliSessionID, "type", env.Type, "err", werr.Error())
		}
	}
}
