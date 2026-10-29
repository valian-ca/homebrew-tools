package paths

import (
	"os"
	"path/filepath"
)

const (
	DirMode  os.FileMode = 0o700
	FileMode os.FileMode = 0o600
)

// TestOverride honors test-only settings only under LANES_TEST=1, so a stray
// variable in a user's shell cannot change behavior.
func TestOverride(name string) (string, bool) {
	if os.Getenv("LANES_TEST") != "1" {
		return "", false
	}
	v := os.Getenv(name)
	return v, v != ""
}

func StateDir() (string, error) {
	if v, ok := TestOverride("LANES_STATE_DIR"); ok {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "lanes"), nil
}

func MustStateDir() string {
	p, err := StateDir()
	if err != nil {
		panic("lanes: cannot resolve the state directory: " + err.Error())
	}
	return p
}

func EnsureDir(dir string) error {
	return os.MkdirAll(dir, DirMode)
}

func Ports() string { return filepath.Join(MustStateDir(), "ports.json") }

func PortsLock() string { return filepath.Join(MustStateDir(), "ports.lock") }

func Socket() string { return filepath.Join(MustStateDir(), "lanesd.sock") }

func Stacks() string { return filepath.Join(MustStateDir(), "stacks.json") }

func DaemonLock() string { return filepath.Join(MustStateDir(), "lanesd.lock") }

func DaemonLog() string { return filepath.Join(MustStateDir(), "lanesd.log") }

func LaneDir(adminDir string) string { return filepath.Join(adminDir, "lanes") }

func LaneRecord(adminDir string) string { return filepath.Join(LaneDir(adminDir), "lane.json") }

func LaneLock(adminDir string) string { return filepath.Join(LaneDir(adminDir), "lane.lock") }

func Activity(adminDir string) string { return filepath.Join(LaneDir(adminDir), "activity") }

func PrepLock(adminDir string) string { return filepath.Join(LaneDir(adminDir), "prep.lock") }

func PrepStamp(adminDir, step string) string {
	return filepath.Join(LaneDir(adminDir), "prep", step+".stamp")
}

func RepoDir(commonDir string) string { return filepath.Join(commonDir, "lanes") }

func Registry(commonDir string) string { return filepath.Join(RepoDir(commonDir), "registry.json") }

func RegistryLock(commonDir string) string { return filepath.Join(RepoDir(commonDir), "registry.lock") }

func Logs(tmpdir string) string { return filepath.Join(tmpdir, "logs") }

func PieceLog(tmpdir, piece string) string { return filepath.Join(Logs(tmpdir), piece+".log") }

func PCSocket(tmpdir string) string { return filepath.Join(tmpdir, "pc.sock") }

func PCLog(tmpdir string) string { return filepath.Join(Logs(tmpdir), "process-compose.log") }

func Fragment(worktree string) string { return filepath.Join(worktree, ".lanes", "adapters.yaml") }

func FragmentDir(worktree string) string { return filepath.Join(worktree, ".lanes") }
