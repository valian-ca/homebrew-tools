// Package sessionstore reads Claude Code Desktop session titles and turns them
// into the same transcript:ai-title / transcript:custom-title events the
// transcript watcher already emits.
//
// Desktop is the one entry point whose titles never reach the JSONL transcript:
// it keeps them in a local session store instead. This store is the second,
// mutually-exclusive title source — a session appears either here (Desktop) or
// in the transcript (terminal / VSCode), never both, so the two sources never
// double-emit (VAL-243).
package sessionstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Entry is the slice of a Desktop store file atelierd acts on: the CLI session
// id (identical to the claudeSessionId carried on every event), the current
// title with its source, and the session's real last-activity time.
//
// ActivityAt is the session's own clock, not the scan's. The derived title
// event is stamped with it so a startup scan of long-idle sessions does not
// re-date them to "now" — which would otherwise resurrect dead sessions and
// shipped cards in the dashboard for the freshness window. Zero when the store
// file carries no usable timestamp; Derive falls back to its Clock then.
type Entry struct {
	CliSessionID string
	Title        string
	TitleSource  string
	ActivityAt   time.Time
}

type storeFile struct {
	CliSessionID   string `json:"cliSessionId"`
	Title          string `json:"title"`
	TitleSource    string `json:"titleSource"`
	LastActivityAt int64  `json:"lastActivityAt"`
	CreatedAt      int64  `json:"createdAt"`
}

// resolveActivityAt picks the session's real activity time from the store file:
// lastActivityAt, falling back to createdAt, both epoch milliseconds. Returns
// the zero time when neither is present so Derive can fall back to its Clock.
func resolveActivityAt(sf storeFile) time.Time {
	switch {
	case sf.LastActivityAt > 0:
		return time.UnixMilli(sf.LastActivityAt).UTC()
	case sf.CreatedAt > 0:
		return time.UnixMilli(sf.CreatedAt).UTC()
	default:
		return time.Time{}
	}
}

// ScanEntries walks the Desktop session store rooted at root and returns one
// Entry per parseable local_*.json. Unreadable, unparseable, or id-less files
// are skipped silently: Claude Desktop owns the store and may rewrite a file
// mid-read or carry shapes we don't recognise, and a single bad file must not
// stall the scan (mirrors transcript.Derive's tolerance of malformed lines).
// A missing root returns no entries and no error — Desktop may be uninstalled
// or simply have no sessions yet.
func ScanEntries(root string) ([]Entry, error) {
	var entries []Entry
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// A per-entry error (an unreadable subdir, a file vanishing
			// mid-walk) must not discard entries already collected: skip it and
			// keep walking, same tolerance as the per-file failures below.
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasPrefix(name, "local_") || !strings.HasSuffix(name, ".json") {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		var sf storeFile
		if jerr := json.Unmarshal(raw, &sf); jerr != nil {
			return nil
		}
		if sf.CliSessionID == "" {
			return nil
		}
		entries = append(entries, Entry{
			CliSessionID: sf.CliSessionID,
			Title:        sf.Title,
			TitleSource:  sf.TitleSource,
			ActivityAt:   resolveActivityAt(sf),
		})
		return nil
	})
	if walkErr != nil {
		if errors.Is(walkErr, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("walk desktop session store: %w", walkErr)
	}
	return entries, nil
}
