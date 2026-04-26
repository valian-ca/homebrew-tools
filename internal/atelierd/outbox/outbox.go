// Package outbox handles the local atomic write queue at ~/.atelier/outbox/.
//
// Producers (`atelierd emit`) call Write(envelope) to drop an event JSON file
// without any network. The consumer (`atelierd run`) calls List + Read +
// Delete to ship those events to Firestore. The atomic-rename pattern (.tmp
// then os.Rename) ensures fsnotify never sees a partial JSON payload.
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

// Envelope is the JSON shape `atelierd emit` writes. host/uid/ts are
// intentionally absent — they are added by `atelierd run` at ship time
// (host from os.Hostname, uid from credentials, ts decoded from ULID prefix).
type Envelope struct {
	ULID            string         `json:"ulid"`
	Type            string         `json:"type"`
	ClaudeSessionID string         `json:"claudeSessionId"`
	Payload         map[string]any `json:"payload"`
	CreatedAt       time.Time      `json:"createdAt"`
}

// Write persists e atomically to ~/.atelier/outbox/<ulid>.json.
// The directory is created (mode 0700) on first write.
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

// List returns every *.json file in the outbox sorted by name (ULID
// lexicographically increasing — chronological order). Files in the middle of
// being written (.tmp suffix) are excluded.
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
