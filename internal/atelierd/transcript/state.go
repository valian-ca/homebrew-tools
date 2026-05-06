package transcript

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/paths"
)

// State is the persisted per-session watcher state. It encodes everything
// needed to resume from a kill -9 without duplicating events already shipped
// (VAL-201 AC 4):
//
//   - Offset       — byte position immediately past the last \n we've fully consumed.
//   - LastMsgID    — last assistant message.id for which we emitted hook:assistant-turn.
//     Same id appears across consecutive JSONL lines when a single
//     turn produces multiple content blocks; the dedup key.
//   - LastPromptID — last promptId for which we emitted hook:user-prompt-submit.
//     Multiple user records share a promptId (initial prompt + any
//     auto-injected attachments / system reminders); we emit once
//     per fresh promptId.
//   - OpenToolUseTools — tool_use_id → tool_name for tool_use blocks whose
//     pre fired but whose post hasn't yet. We need the
//     tool name on the post side because toolUseResult
//     records don't repeat it.
//   - ClosedToolUseIDs — tool_use_ids whose post-tool-use was already
//     emitted. Kept (not pruned) so a replay after
//     kill -9 between outbox.Write and SaveState
//     doesn't re-emit the pre or the post (AC 4).
type State struct {
	ClaudeSessionID  string            `json:"claudeSessionId"`
	JSONLPath        string            `json:"jsonlPath"`
	Offset           int64             `json:"offset"`
	LastMsgID        string            `json:"lastMsgId,omitempty"`
	LastPromptID     string            `json:"lastPromptId,omitempty"`
	OpenToolUseTools map[string]string `json:"openToolUseTools,omitempty"`
	ClosedToolUseIDs map[string]bool   `json:"closedToolUseIds,omitempty"`
	LastActivityAt   time.Time         `json:"lastActivityAt"`
}

// SessionsDir returns ~/.atelier/sessions. The directory is created on first
// write per the same EnsureDir pattern used by paths.Outbox().
func SessionsDir() string { return filepath.Join(paths.MustRoot(), "sessions") }

// SessionFile returns the per-session state file path.
func SessionFile(claudeSessionID string) string {
	return filepath.Join(SessionsDir(), claudeSessionID+".json")
}

// LoadState reads a persisted state file. Returns os.ErrNotExist when the
// session has never been registered. Callers can probe via errors.Is.
func LoadState(claudeSessionID string) (*State, error) {
	bytes, err := os.ReadFile(SessionFile(claudeSessionID))
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(bytes, &s); err != nil {
		return nil, fmt.Errorf("parse session state %s: %w", claudeSessionID, err)
	}
	if s.OpenToolUseTools == nil {
		s.OpenToolUseTools = map[string]string{}
	}
	if s.ClosedToolUseIDs == nil {
		s.ClosedToolUseIDs = map[string]bool{}
	}
	return &s, nil
}

// SaveState writes s atomically to ~/.atelier/sessions/<id>.json (mode 0600).
// The .tmp + os.Rename dance keeps a partially-written state file from being
// observed by a peer or by the daemon's own restart path.
func SaveState(s *State) error {
	if s.ClaudeSessionID == "" {
		return errors.New("save state: claudeSessionID is empty")
	}
	if err := paths.EnsureDir(SessionsDir()); err != nil {
		return fmt.Errorf("ensure sessions dir: %w", err)
	}
	if s.OpenToolUseTools == nil {
		s.OpenToolUseTools = map[string]string{}
	}
	if s.ClosedToolUseIDs == nil {
		s.ClosedToolUseIDs = map[string]bool{}
	}
	bytes, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshal session state: %w", err)
	}
	target := SessionFile(s.ClaudeSessionID)
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, bytes, paths.FileMode); err != nil {
		return fmt.Errorf("write session tempfile: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename session state file: %w", err)
	}
	return nil
}

// ListStates returns every persisted session state on disk, sorted by file
// name. Used by the daemon at startup to rehydrate watchers for sessions
// that were active when the daemon was last running.
func ListStates() ([]*State, error) {
	entries, err := os.ReadDir(SessionsDir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read sessions dir: %w", err)
	}
	var states []*State
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if filepath.Ext(name) != ".json" {
			continue
		}
		id := name[:len(name)-len(".json")]
		s, err := LoadState(id)
		if err != nil {
			// A corrupt state file shouldn't block other sessions — log via
			// the caller; here we just skip.
			continue
		}
		states = append(states, s)
	}
	return states, nil
}
