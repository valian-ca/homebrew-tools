// Package credentials persists the Firebase Auth tokens that atelierd holds
// after a successful `atelierd link`. The file lives at ~/.atelier/credentials
// (mode 0600) and is read by every sub-command that needs to authenticate
// against Firebase (run, status, unlink, refresh).
package credentials

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/paths"
)

// Credentials is the on-disk shape persisted at ~/.atelier/credentials.
type Credentials struct {
	UID              string    `json:"uid"`
	Email            string    `json:"email"`
	IDToken          string    `json:"idToken"`
	RefreshToken     string    `json:"refreshToken"`
	IDTokenExpiresAt time.Time `json:"idTokenExpiresAt"`
}

// ErrNotLinked is returned by Load when the credentials file does not exist —
// callers can treat it as "the daemon is not linked" without needing to inspect
// the underlying os error.
var ErrNotLinked = errors.New("atelierd: not linked (no ~/.atelier/credentials)")

// Load reads ~/.atelier/credentials and parses the JSON. Returns ErrNotLinked
// if the file is absent.
func Load() (*Credentials, error) {
	bytes, err := os.ReadFile(paths.Credentials())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotLinked
		}
		return nil, fmt.Errorf("read credentials: %w", err)
	}
	var c Credentials
	if err := json.Unmarshal(bytes, &c); err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}
	return &c, nil
}

// Save writes c atomically to ~/.atelier/credentials with mode 0600.
// Atomic = write to a tempfile in the same dir, fsync, then rename.
func Save(c *Credentials) error {
	if err := paths.EnsureDir(paths.MustRoot()); err != nil {
		return fmt.Errorf("ensure ~/.atelier: %w", err)
	}
	bytes, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}
	target := paths.Credentials()
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, bytes, paths.FileMode); err != nil {
		return fmt.Errorf("write tempfile: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename credentials: %w", err)
	}
	// Belt and suspenders: re-chmod after rename in case umask interfered.
	_ = os.Chmod(target, paths.FileMode)
	return nil
}

// Delete removes ~/.atelier/credentials. Idempotent — returns nil if absent.
func Delete() error {
	if err := os.Remove(paths.Credentials()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Exists reports whether the credentials file is present and readable.
func Exists() bool {
	info, err := os.Stat(paths.Credentials())
	return err == nil && !info.IsDir()
}
