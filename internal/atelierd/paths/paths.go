// Package paths centralises every filesystem location atelierd reads or writes.
//
// All paths live under ~/.atelier/ — an explicit deviation from the tap repo's
// general-tool convention ($XDG_CONFIG_HOME/valian/), motivated by cohesion
// with the atelier sub-domain. Documented in VAL-164 amendment #7.
package paths

import (
	"os"
	"path/filepath"
)

const (
	// DirMode is the owner-only mode for the atelier directory + outbox.
	DirMode os.FileMode = 0o700
	// FileMode is the owner-only mode for credentials, status, log files.
	FileMode os.FileMode = 0o600
)

// Root returns ~/.atelier (without trailing slash).
func Root() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".atelier"), nil
}

// MustRoot is Root() that panics. Use only at init / startup paths.
func MustRoot() string {
	p, err := Root()
	if err != nil {
		panic("atelierd: cannot resolve ~/.atelier: " + err.Error())
	}
	return p
}

// EnsureDir creates dir (and parents) with DirMode if absent.
func EnsureDir(dir string) error {
	return os.MkdirAll(dir, DirMode)
}

// Outbox returns ~/.atelier/outbox.
func Outbox() string { return filepath.Join(MustRoot(), "outbox") }

// Credentials returns ~/.atelier/credentials.
func Credentials() string { return filepath.Join(MustRoot(), "credentials") }

// Status returns ~/.atelier/status.
func Status() string { return filepath.Join(MustRoot(), "status") }

// Log returns ~/.atelier/atelierd.log.
func Log() string { return filepath.Join(MustRoot(), "atelierd.log") }

// OutboxFile returns ~/.atelier/outbox/<ulid>.json.
func OutboxFile(ulid string) string { return filepath.Join(Outbox(), ulid+".json") }
