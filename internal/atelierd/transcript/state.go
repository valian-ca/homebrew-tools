package transcript

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/paths"
)

// State is the persisted per-session watcher state. It encodes everything
// needed to resume from a kill -9 without duplicating events already shipped:
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
//   - LastTitle / LastTitleType — the last (title, event-type) pair emitted as
//     a transcript:ai-title / transcript:custom-title. The dedup key for
//     titles: unlike the other records a title line carries no id, and the
//     file is re-read from offset 0 whenever it is detected as truncated (see
//     consume). Without this key an unchanged title re-emits on every replay,
//     stamped at re-read time — which advances the card's lastEventAt and
//     resurrects shipped/finished cards on the dashboard. Emit only on a real
//     change (title or kind).
//
// WatcherKey distinguishes the on-disk file basis from the ClaudeSessionID
// emitted on envelopes. Empty for parent transcripts (file basis =
// ClaudeSessionID, layout unchanged). For subagent transcripts the key is
// "<parentSessionID>/subagents/<agentFileBase>" — the state lives at
// ~/.atelier/sessions/<parentSessionID>/subagents/<agentFileBase>.json
// while envelopes still carry the parent's ClaudeSessionID, so the backend's
// aggregatePhaseRun folds subagent tokens into the parent's phaseRun without
// any backend-side change or new event type.
type State struct {
	ClaudeSessionID  string            `json:"claudeSessionId"`
	WatcherKey       string            `json:"watcherKey,omitempty"`
	JSONLPath        string            `json:"jsonlPath"`
	Offset           int64             `json:"offset"`
	LastMsgID        string            `json:"lastMsgId,omitempty"`
	LastPromptID     string            `json:"lastPromptId,omitempty"`
	OpenToolUseTools map[string]string `json:"openToolUseTools,omitempty"`
	ClosedToolUseIDs map[string]bool   `json:"closedToolUseIds,omitempty"`
	LastTitle        string            `json:"lastTitle,omitempty"`
	LastTitleType    string            `json:"lastTitleType,omitempty"`
	LastActivityAt   time.Time         `json:"lastActivityAt"`
}

// Key returns the on-disk file basis: WatcherKey when non-empty, otherwise
// ClaudeSessionID (parent). Maps directly to a path under SessionsDir() via
// SessionFile.
func (s *State) Key() string {
	if s.WatcherKey != "" {
		return s.WatcherKey
	}
	return s.ClaudeSessionID
}

func (s *State) IsSubagent() bool { return s.WatcherKey != "" }

// ParentSessionID returns the registered parent's ClaudeSessionID — for a
// parent state that's its own ID; for a subagent it's the first segment of
// WatcherKey. The parent's ID is the pivot every subagent envelope and
// orphan check anchors on.
func (s *State) ParentSessionID() string {
	if s.WatcherKey == "" {
		return s.ClaudeSessionID
	}
	parent, _, _ := strings.Cut(s.WatcherKey, "/")
	return parent
}

// SubagentWatcherKey returns the on-disk key for a subagent state file, of
// the form "<parentSessionID>/subagents/<agentFileBase>". Sole construction
// path so callers don't reach into the encoding directly.
func SubagentWatcherKey(parentSessionID, agentFileBase string) string {
	return filepath.Join(parentSessionID, "subagents", agentFileBase)
}

// SessionsDir returns ~/.atelier/sessions. Created on first write per the
// EnsureDir pattern used by paths.Outbox().
func SessionsDir() string { return filepath.Join(paths.MustRoot(), "sessions") }

// SessionFile returns the per-session state file path for a key. Parents
// pass their ClaudeSessionID; subagents pass "<parentSessionID>/subagents/<agentFileBase>".
func SessionFile(key string) string {
	return filepath.Join(SessionsDir(), key+".json")
}

// validateKey rejects any key that is not a single safe segment (parent) or
// the exact <parent>/subagents/<agentBase> shape (subagent). Defense-in-depth
// at the trust boundary: the key flows into filepath.Join + os.MkdirAll +
// os.Rename, so refusing "..", empty segments, NUL bytes and path separators
// other than the one we expect prevents a malformed claudeSessionId from
// writing state files outside ~/.atelier/sessions/.
func validateKey(key string) error {
	parts := strings.Split(key, "/")
	if len(parts) != 1 && (len(parts) != 3 || parts[1] != "subagents") {
		return fmt.Errorf("invalid session key shape: %q", key)
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return fmt.Errorf("invalid segment %q in key %q", p, key)
		}
		if strings.ContainsAny(p, `\:`+"\x00") {
			return fmt.Errorf("invalid characters in segment %q of key %q", p, key)
		}
	}
	return nil
}

// LoadState reads a persisted state file. Returns os.ErrNotExist when the
// session has never been registered. Callers can probe via errors.Is.
func LoadState(key string) (*State, error) {
	if err := validateKey(key); err != nil {
		return nil, err
	}
	bytes, err := os.ReadFile(SessionFile(key))
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(bytes, &s); err != nil {
		return nil, fmt.Errorf("parse session state %s: %w", key, err)
	}
	if s.OpenToolUseTools == nil {
		s.OpenToolUseTools = map[string]string{}
	}
	if s.ClosedToolUseIDs == nil {
		s.ClosedToolUseIDs = map[string]bool{}
	}
	return &s, nil
}

// SaveState writes s atomically to its keyed location (mode 0600). Intermediate
// directories (e.g. ~/.atelier/sessions/<parent>/subagents/) are created on
// first write. The .tmp + os.Rename dance keeps a partially-written state
// file from being observed by a peer or by the daemon's own restart path.
func SaveState(s *State) error {
	if s.ClaudeSessionID == "" {
		return errors.New("save state: claudeSessionID is empty")
	}
	if err := validateKey(s.Key()); err != nil {
		return err
	}
	target := SessionFile(s.Key())
	if err := paths.EnsureDir(filepath.Dir(target)); err != nil {
		return fmt.Errorf("ensure session state dir: %w", err)
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

// DeleteState removes a persisted state file. Empty directories left behind
// by a subagent deletion (<parent>/subagents/, then <parent>/) are pruned so
// GC leaves no skeleton tree; the prune stops at the first non-empty
// directory and never climbs past SessionsDir().
func DeleteState(key string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	if err := os.Remove(SessionFile(key)); err != nil {
		return err
	}
	root := SessionsDir()
	for dir := filepath.Dir(SessionFile(key)); dir != root && strings.HasPrefix(dir, root+string(filepath.Separator)); dir = filepath.Dir(dir) {
		if err := os.Remove(dir); err != nil {
			break
		}
	}
	return nil
}

// ListStates returns every persisted session state on disk, sorted by key.
// Walks the sessions tree to depth 2 — top-level <id>.json (parents) and
// <parentId>/subagents/<agentBase>.json (subagents). Files at any other depth
// are skipped: the daemon does not yet support nested layouts and a future
// Anthropic format change should not silently spawn watchers in unexpected
// shapes.
func ListStates() ([]*State, error) {
	root := SessionsDir()
	if _, err := os.Stat(root); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat sessions dir: %w", err)
	}
	var states []*State

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		relSlash := filepath.ToSlash(rel)
		parts := strings.Split(relSlash, "/")

		if d.IsDir() {
			switch {
			case len(parts) == 1:
				return nil
			case len(parts) == 2 && parts[1] == "subagents":
				return nil
			default:
				return fs.SkipDir
			}
		}

		name := d.Name()
		if !strings.HasSuffix(name, ".json") {
			return nil
		}
		switch {
		case len(parts) == 1:
		case len(parts) == 3 && parts[1] == "subagents":
		default:
			return nil
		}

		key := strings.TrimSuffix(relSlash, ".json")
		s, lerr := LoadState(key)
		if lerr != nil {
			return nil
		}
		states = append(states, s)
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("walk sessions dir: %w", walkErr)
	}
	sort.Slice(states, func(i, j int) bool { return states[i].Key() < states[j].Key() })
	return states, nil
}
