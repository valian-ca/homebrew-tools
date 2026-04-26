// Package status reads and writes ~/.atelier/status — the JSON heartbeat file
// the daemon refreshes every 30s and `atelierd status` reads to report health.
package status

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/paths"
)

// AuthState reflects whether the daemon currently holds a valid Firebase
// session. Written by the run loop; read by `atelierd status` (which surfaces
// FAIL on auth-lost) and by `atelierd run` itself when restarting after a
// crash.
type AuthState string

const (
	// AuthOk — daemon has a fresh idToken (or can refresh it).
	AuthOk AuthState = "ok"
	// AuthLost — Firebase Auth returned 401/403 on token refresh; ship and
	// heartbeat loops have stopped, outbox is accumulating.
	AuthLost AuthState = "auth-lost"
)

// File is the on-disk shape of ~/.atelier/status.
type File struct {
	Version          string    `json:"version"`
	UID              string    `json:"uid"`
	Host             string    `json:"host"`
	LastTickAt       time.Time `json:"lastTickAt"`
	LastHeartbeatAt  time.Time `json:"lastHeartbeatAt"`
	LastShipAt       time.Time `json:"lastShipAt"`
	OutboxBacklog    int       `json:"outboxBacklog"`
	AuthState        AuthState `json:"authState"`
	IDTokenExpiresAt time.Time `json:"idTokenExpiresAt"`
}

// Load reads ~/.atelier/status. Returns nil, nil if the file is absent
// (daemon hasn't started yet); the typed nil tells `atelierd status` to
// report a WARN on the watcher check rather than crash.
func Load() (*File, error) {
	bytes, err := os.ReadFile(paths.Status())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read status: %w", err)
	}
	var f File
	if err := json.Unmarshal(bytes, &f); err != nil {
		return nil, fmt.Errorf("parse status: %w", err)
	}
	return &f, nil
}

// Save writes f atomically to ~/.atelier/status with mode 0600.
func Save(f *File) error {
	if err := paths.EnsureDir(paths.MustRoot()); err != nil {
		return fmt.Errorf("ensure ~/.atelier: %w", err)
	}
	bytes, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal status: %w", err)
	}
	target := paths.Status()
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, bytes, paths.FileMode); err != nil {
		return fmt.Errorf("write tempfile: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename status: %w", err)
	}
	_ = os.Chmod(target, paths.FileMode)
	return nil
}
