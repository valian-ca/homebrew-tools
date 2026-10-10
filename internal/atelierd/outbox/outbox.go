package outbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/paths"
)

// Envelope is the JSON shape queued in the outbox. host and uid are absent —
// `atelierd run` adds them at ship time. TS is set only by producers whose
// event time is not the ULID prefix: an activity:minute heartbeat is keyed
// <claudeSessionId>_<minute>, not by a ULID, and its ts is that minute.
type Envelope struct {
	ULID            string         `json:"ulid"`
	Type            string         `json:"type"`
	ClaudeSessionID string         `json:"claudeSessionId"`
	Payload         map[string]any `json:"payload"`
	CreatedAt       time.Time      `json:"createdAt"`
	TS              *time.Time     `json:"ts,omitempty"`
}

func Write(e *Envelope) error {
	if err := paths.EnsureDir(paths.Outbox()); err != nil {
		return fmt.Errorf("ensure outbox dir: %w", err)
	}
	bytes, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	target := paths.OutboxFile(e.ULID)
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, bytes, paths.FileMode); err != nil {
		return fmt.Errorf("write tempfile: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename outbox file: %w", err)
	}
	return nil
}

// Name order is chronological for ULID-keyed events only; the backend orders heartbeats by ts.
func List() ([]string, error) {
	entries, err := os.ReadDir(paths.Outbox())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read outbox dir: %w", err)
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		files = append(files, filepath.Join(paths.Outbox(), name))
	}
	sort.Strings(files)
	return files, nil
}

// Count returns the number of *.json files in the outbox. Used by `atelierd
// status` to report backlog. A separate path from List avoids the allocation
// when only the count matters.
func Count() (int, error) {
	entries, err := os.ReadDir(paths.Outbox())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("read outbox dir: %w", err)
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			n++
		}
	}
	return n, nil
}

func CountRejected() (int, error) {
	entries, err := os.ReadDir(paths.Outbox())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("read outbox dir: %w", err)
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json.rejected") {
			n++
		}
	}
	return n, nil
}

// Read parses a single outbox JSON file.
func Read(path string) (*Envelope, error) {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read outbox file: %w", err)
	}
	var e Envelope
	if err := json.Unmarshal(bytes, &e); err != nil {
		return nil, fmt.Errorf("parse outbox file %s: %w", filepath.Base(path), err)
	}
	return &e, nil
}

// Delete removes path. Idempotent.
func Delete(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
