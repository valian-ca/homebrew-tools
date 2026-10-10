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

	"golang.org/x/sys/unix"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/paths"
)

// SchemaVersion marks a state that holds one record per session tree. A state
// file without it was written by atelierd ≤ 0.18.x, one file per transcript.
const SchemaVersion = 2

// emittedMinutesWindow bounds EmittedMinutes on disk. A minute older than this
// that a truncation makes us re-derive collides on its Firestore doc (409),
// which the shipper counts as shipped.
const emittedMinutesWindow = 24 * time.Hour

// State is the persisted read state of one session tree (the parent
// transcript plus its subagent and Workflow transcripts).
//
// Offsets is keyed by the transcript path relative to the parent's directory
// ("<id>.jsonl", "<id>/subagents/workflows/wf_x/agent-y.jsonl"); each value is
// the byte position just past the last complete line consumed. EmittedMinutes
// is keyed by the UTC minute ("200601021504") of every heartbeat already
// queued, so a line re-read after a crash or a truncation never queues its
// minute twice.
type State struct {
	ClaudeSessionID string           `json:"claudeSessionId"`
	JSONLPath       string           `json:"jsonlPath"`
	Offsets         map[string]int64 `json:"offsets"`
	EmittedMinutes  map[string]bool  `json:"emittedMinutes"`
	LastTitle       string           `json:"lastTitle,omitempty"`
	LastTitleType   string           `json:"lastTitleType,omitempty"`
	LastActivityAt  time.Time        `json:"lastActivityAt"`
	SchemaVersion   int              `json:"schemaVersion"`

	// LegacyParentOffset mirrors the parent's offset under the 0.18.x key so
	// a 0.18.x daemon still running after a manual `brew upgrade` resumes
	// where this version stopped instead of re-reading from 0.
	LegacyParentOffset int64 `json:"offset"`
}

func (s *State) ensureMaps() {
	if s.Offsets == nil {
		s.Offsets = map[string]int64{}
	}
	if s.EmittedMinutes == nil {
		s.EmittedMinutes = map[string]bool{}
	}
}

func (s *State) markMinute(minute time.Time) bool {
	s.ensureMaps()
	key := minuteKey(minute)
	if s.EmittedMinutes[key] {
		return false
	}
	s.EmittedMinutes[key] = true
	return true
}

// ForgetMinute undoes the record of a heartbeat the caller decided not to
// queue, so a later line in that minute can still emit it.
func (s *State) ForgetMinute(minute time.Time) {
	delete(s.EmittedMinutes, minuteKey(minute))
}

func (s *State) pruneEmittedMinutes() {
	newest := ""
	for k := range s.EmittedMinutes {
		if k > newest {
			newest = k
		}
	}
	if newest == "" {
		return
	}
	t, err := time.Parse(minuteKeyLayout, newest)
	if err != nil {
		return
	}
	cutoff := minuteKey(t.Add(-emittedMinutesWindow))
	for k := range s.EmittedMinutes {
		if k < cutoff {
			delete(s.EmittedMinutes, k)
		}
	}
}

// SubagentsDir is the directory Claude Code creates next to the parent
// transcript for Task subagents and Workflow agents.
func (s *State) SubagentsDir() string {
	return filepath.Join(strings.TrimSuffix(s.JSONLPath, ".jsonl"), "subagents")
}

func (s *State) OffsetKey(path string) string {
	rel, err := filepath.Rel(filepath.Dir(s.JSONLPath), path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

func (s *State) Offset(path string) int64 { return s.Offsets[s.OffsetKey(path)] }

func (s *State) SetOffset(path string, offset int64) {
	s.ensureMaps()
	s.Offsets[s.OffsetKey(path)] = offset
}

// TreeFiles lists the transcripts of the session tree: the parent, then every
// agent-*.jsonl at any depth under SubagentsDir. Workflow journals, meta and
// forked-skill files share the directory and are not transcripts.
func (s *State) TreeFiles() []string {
	files := []string{s.JSONLPath}
	_ = filepath.WalkDir(s.SubagentsDir(), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, "agent-") && strings.HasSuffix(name, ".jsonl") {
			files = append(files, path)
		}
		return nil
	})
	return files
}

// SessionsDir returns ~/.atelier/sessions. Created on first write per the
// EnsureDir pattern used by paths.Outbox().
func SessionsDir() string { return filepath.Join(paths.MustRoot(), "sessions") }

func SessionFile(claudeSessionID string) string {
	return filepath.Join(SessionsDir(), claudeSessionID+".json")
}

func legacySubagentsDir(claudeSessionID string) string {
	return filepath.Join(SessionsDir(), claudeSessionID)
}

// validateKey rejects any session id that is not a single safe path segment.
// The id flows into filepath.Join, os.Rename and os.RemoveAll, so "..", path
// separators and NUL bytes would let a malformed claudeSessionId write or
// delete outside ~/.atelier/sessions/.
func validateKey(key string) error {
	if key == "" || key == "." || key == ".." {
		return fmt.Errorf("invalid session key %q", key)
	}
	if strings.ContainsAny(key, `/\:`+"\x00") {
		return fmt.Errorf("invalid characters in session key %q", key)
	}
	return nil
}

// withSessionsLock serializes every load-modify-save of a session state
// across the daemon's goroutines and `atelierd emit` processes: two
// concurrent migrations, or a re-registration racing the reader's save,
// would otherwise each save what the other just lost.
func withSessionsLock(fn func() error) error {
	if err := paths.EnsureDir(SessionsDir()); err != nil {
		return fmt.Errorf("ensure session state dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(SessionsDir(), "sessions.lock"), os.O_RDWR|os.O_CREATE, paths.FileMode)
	if err != nil {
		return fmt.Errorf("open sessions lock: %w", err)
	}
	defer f.Close()
	for {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("lock sessions: %w", err)
	}
	defer func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }()
	return fn()
}

// LoadState reads a persisted state, migrating a 0.18.x layout on the fly.
// Returns an error wrapping os.ErrNotExist when the session is not registered.
func LoadState(claudeSessionID string) (*State, error) {
	if err := validateKey(claudeSessionID); err != nil {
		return nil, err
	}
	s, err := readState(claudeSessionID)
	if err != nil || s.SchemaVersion >= SchemaVersion {
		return s, err
	}
	var loadErr error
	if lockErr := withSessionsLock(func() error {
		s, loadErr = loadStateLocked(claudeSessionID)
		return nil
	}); lockErr != nil {
		return nil, lockErr
	}
	return s, loadErr
}

// UpdateState runs fn on the current state of a session under the sessions
// lock and saves the state fn returns; a nil state saves nothing. fn receives
// the load error, os.ErrNotExist included, so it decides how to register.
func UpdateState(claudeSessionID string, fn func(current *State, loadErr error) *State) error {
	if err := validateKey(claudeSessionID); err != nil {
		return err
	}
	return withSessionsLock(func() error {
		next := fn(loadStateLocked(claudeSessionID))
		if next == nil {
			return nil
		}
		return saveStateLocked(next)
	})
}

func readState(claudeSessionID string) (*State, error) {
	raw, err := os.ReadFile(SessionFile(claudeSessionID))
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("parse session state %s: %w", claudeSessionID, err)
	}
	s.ensureMaps()
	return &s, nil
}

func loadStateLocked(claudeSessionID string) (*State, error) {
	s, err := readState(claudeSessionID)
	if err != nil || s.SchemaVersion >= SchemaVersion {
		return s, err
	}
	return migrateLegacyState(claudeSessionID)
}

type legacyState struct {
	ClaudeSessionID string    `json:"claudeSessionId"`
	JSONLPath       string    `json:"jsonlPath"`
	Offset          int64     `json:"offset"`
	LastTitle       string    `json:"lastTitle,omitempty"`
	LastTitleType   string    `json:"lastTitleType,omitempty"`
	LastActivityAt  time.Time `json:"lastActivityAt"`
}

// migrateLegacyState folds a 0.18.x parent state and its per-subagent state
// files into one tree state, carrying every offset over: an upgrade must not
// re-read, and so re-emit, any transcript. The subagent states stay on disk
// until the state GC prunes them, so a 0.18.x daemon still running after a
// manual `brew upgrade` keeps its offsets. A state without a transcript is
// the retired OpenCode signature; it is deleted with no session end
// synthesized.
func migrateLegacyState(claudeSessionID string) (*State, error) {
	raw, err := os.ReadFile(SessionFile(claudeSessionID))
	if err != nil {
		return nil, err
	}
	var legacy legacyState
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return nil, fmt.Errorf("parse legacy session state %s: %w", claudeSessionID, err)
	}
	if legacy.JSONLPath == "" {
		if err := deleteStateLocked(claudeSessionID); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("delete transcript-less state %s: %w", claudeSessionID, err)
		}
		return nil, fmt.Errorf("transcript-less session %s retired: %w", claudeSessionID, os.ErrNotExist)
	}

	s := &State{
		ClaudeSessionID: claudeSessionID,
		JSONLPath:       legacy.JSONLPath,
		LastTitle:       legacy.LastTitle,
		LastTitleType:   legacy.LastTitleType,
		LastActivityAt:  legacy.LastActivityAt,
	}
	s.SetOffset(legacy.JSONLPath, legacy.Offset)

	subStates, _ := filepath.Glob(filepath.Join(legacySubagentsDir(claudeSessionID), "subagents", "*.json"))
	for _, f := range subStates {
		subRaw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var sub legacyState
		if err := json.Unmarshal(subRaw, &sub); err != nil || sub.JSONLPath == "" {
			continue
		}
		s.SetOffset(sub.JSONLPath, sub.Offset)
	}

	if err := saveStateLocked(s); err != nil {
		return nil, fmt.Errorf("save migrated session state %s: %w", claudeSessionID, err)
	}
	return s, nil
}

// SaveState writes s atomically (mode 0600) under the sessions lock.
func SaveState(s *State) error {
	if s.ClaudeSessionID == "" {
		return errors.New("save state: claudeSessionID is empty")
	}
	if err := validateKey(s.ClaudeSessionID); err != nil {
		return err
	}
	return withSessionsLock(func() error { return saveStateLocked(s) })
}

// saveStateLocked gives each write its own temp file: a shared .tmp let
// concurrent writers interleave their bytes into an unparseable state, which
// stops the session's reader.
func saveStateLocked(s *State) error {
	if s.ClaudeSessionID == "" {
		return errors.New("save state: claudeSessionID is empty")
	}
	s.ensureMaps()
	s.pruneEmittedMinutes()
	s.SchemaVersion = SchemaVersion
	s.LegacyParentOffset = s.Offset(s.JSONLPath)
	raw, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshal session state: %w", err)
	}
	target := SessionFile(s.ClaudeSessionID)
	tmp, err := os.CreateTemp(SessionsDir(), s.ClaudeSessionID+".json.*.tmp")
	if err != nil {
		return fmt.Errorf("create session tempfile: %w", err)
	}
	_, werr := tmp.Write(raw)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("write session tempfile: %w", errors.Join(werr, cerr))
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("rename session state file: %w", err)
	}
	return nil
}

// DeleteState removes a session's state file and any 0.18.x subagent state
// directory left beside it.
func DeleteState(claudeSessionID string) error {
	if err := validateKey(claudeSessionID); err != nil {
		return err
	}
	return withSessionsLock(func() error { return deleteStateLocked(claudeSessionID) })
}

func deleteStateLocked(claudeSessionID string) error {
	err := os.Remove(SessionFile(claudeSessionID))
	_ = os.RemoveAll(legacySubagentsDir(claudeSessionID))
	return err
}

// ListStates returns every persisted session state, sorted by session id,
// migrating 0.18.x states as it reads them. Unreadable states are skipped.
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
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		s, lerr := LoadState(strings.TrimSuffix(name, ".json"))
		if lerr != nil {
			continue
		}
		states = append(states, s)
	}
	sort.Slice(states, func(i, j int) bool { return states[i].ClaudeSessionID < states[j].ClaudeSessionID })
	return states, nil
}

// PruneLegacyDirs removes the 0.18.x subagent state directories that no
// longer serve: their parent state is gone or already migrated. A directory
// whose parent is still in the 0.18.x layout waits for its migration.
func PruneLegacyDirs() (int, error) {
	removed := 0
	err := withSessionsLock(func() error {
		entries, err := os.ReadDir(SessionsDir())
		if err != nil {
			return fmt.Errorf("read sessions dir: %w", err)
		}
		for _, e := range entries {
			if !e.IsDir() || validateKey(e.Name()) != nil {
				continue
			}
			parent, perr := readState(e.Name())
			if perr == nil && parent.SchemaVersion < SchemaVersion {
				continue
			}
			if perr != nil && !errors.Is(perr, os.ErrNotExist) {
				continue
			}
			if os.RemoveAll(legacySubagentsDir(e.Name())) == nil {
				removed++
			}
		}
		return nil
	})
	return removed, err
}
