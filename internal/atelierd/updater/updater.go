// Package updater performs targeted, zero-touch self-update of the atelierd
// binary through Homebrew. It is deliberately decoupled from Firebase auth and
// credentials so an associate whose token has lapsed still receives updates.
package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Formula is the fully-qualified tap formula the daemon keeps itself on.
const Formula = "valian-ca/tools/atelierd"

// defaultBrewProbes are the standard Homebrew install roots, Apple Silicon
// first then Intel. Probed only when HOMEBREW_PREFIX is unset.
var defaultBrewProbes = []string{"/opt/homebrew/bin/brew", "/usr/local/bin/brew"}

// runner executes a command and returns its combined output. A field so tests
// can substitute a fake without spawning brew.
type runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Updater upgrades the atelierd formula in place via a located brew binary.
type Updater struct {
	brewPath string
	run      runner
}

// New locates brew and returns an Updater, or an error if brew can't be found.
func New() (*Updater, error) {
	bp, err := BrewPath()
	if err != nil {
		return nil, err
	}
	return &Updater{brewPath: bp, run: execRun}, nil
}

func execRun(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// BrewPath resolves the brew executable. Under launchd the daemon's PATH lacks
// the Homebrew bin dir, so an explicit lookup is required.
func BrewPath() (string, error) {
	return brewPathFrom(os.Getenv("HOMEBREW_PREFIX"), defaultBrewProbes)
}

func brewPathFrom(prefix string, probes []string) (string, error) {
	if prefix != "" {
		if candidate := filepath.Join(prefix, "bin", "brew"); isExecutable(candidate) {
			return candidate, nil
		}
	}
	for _, candidate := range probes {
		if isExecutable(candidate) {
			return candidate, nil
		}
	}
	return "", errors.New("brew not found (set HOMEBREW_PREFIX or install Homebrew)")
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir() && info.Mode()&0o111 != 0
}

// Upgrade refreshes Homebrew metadata and upgrades the atelierd formula. It
// targets only that formula and leaves the running process alone: brew upgrade
// relinks the new keg but never restarts the brew service, so the caller is
// free to restart on its own terms afterwards.
func (u *Updater) Upgrade(ctx context.Context) error {
	if out, err := u.run(ctx, u.brewPath, "update"); err != nil {
		return fmt.Errorf("brew update: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := u.run(ctx, u.brewPath, "upgrade", Formula); err != nil {
		return fmt.Errorf("brew upgrade %s: %w: %s", Formula, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// InstalledVersion reports the version of the freshly-linked binary, which
// after an Upgrade may differ from the running process's compiled-in version.
func (u *Updater) InstalledVersion(ctx context.Context) (string, error) {
	prefix := filepath.Dir(filepath.Dir(u.brewPath))
	bin := filepath.Join(prefix, "bin", "atelierd")
	out, err := u.run(ctx, bin, "--version")
	if err != nil {
		return "", fmt.Errorf("read installed version: %w", err)
	}
	return parseVersion(string(out)), nil
}

// parseVersion pulls the bare version token out of cobra's "atelierd version
// X.Y.Z" output so it can be compared to the compiled-in version string.
func parseVersion(out string) string {
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}
