package sessionstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/paths"
)

// State is the persisted last-emitted title for one Desktop session. It exists
// purely to dedup: the store file is rewritten on every session-state change
// (lastActivityAt, completedTurns, …) — far more often than the title changes —
// so without persisted state the watcher would re-emit a title event on every
// unrelated rewrite and again on every daemon restart.
type State struct {
	CliSessionID    string    `json:"cliSessionId"`
	LastTitle       string    `json:"lastTitle"`
	LastTitleSource string    `json:"lastTitleSource"`
	LastActivityAt  time.Time `json:"lastActivityAt"`
}

func stateFile(cliSessionID string) string {
	return filepath.Join(paths.SessionTitles(), cliSessionID+".json")
}

// validateID rejects any id that is not a single safe path segment. The id is
// Claude Desktop's cliSessionId (a UUID) but it flows into filepath.Join +
// os.Rename, so refusing separators, "..", and NUL keeps a malformed value from
// writing state files outside ~/.atelier/session-titles/ (same trust-boundary
// defense as transcript.validateKey).
func validateID(id string) error {
	if id == "" || id == "." || id == ".." {
		return fmt.Errorf("invalid session id: %q", id)
	}
	if strings.ContainsAny(id, `/\:`+"\x00") {
		return fmt.Errorf("invalid characters in session id: %q", id)
	}
	return nil
}

// LoadState reads a persisted state file. Returns os.ErrNotExist when the
// session has never emitted a title; callers probe via errors.Is.
func LoadState(cliSessionID string) (*State, error) {
	if err := validateID(cliSessionID); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(stateFile(cliSessionID))
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("parse session-title state %s: %w", cliSessionID, err)
	}
	return &s, nil
}

// SaveState writes s atomically (mode 0600) via the .tmp + os.Rename dance so a
// peer or the daemon's restart path never observes a half-written file.
func SaveState(s *State) error {
	if s.CliSessionID == "" {
		return errors.New("save session-title state: cliSessionID is empty")
	}
	if err := validateID(s.CliSessionID); err != nil {
		return err
	}
	if err := paths.EnsureDir(paths.SessionTitles()); err != nil {
		return fmt.Errorf("ensure session-titles dir: %w", err)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshal session-title state: %w", err)
	}
	target := stateFile(s.CliSessionID)
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, raw, paths.FileMode); err != nil {
		return fmt.Errorf("write session-title tempfile: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename session-title state file: %w", err)
	}
	return nil
}
