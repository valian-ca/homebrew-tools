package devicebank

import (
	"os"
	"os/exec"
	"syscall"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/paths"
)

// spawnDetached starts name in its own session with stdio appended to
// ~/.atelier/atelierd.log, so the child survives the CLI process exiting —
// the mechanism behind release's immediate return (recycle runs detached)
// and the emulator process outliving its launcher. A nil env inherits the
// parent's environment.
func spawnDetached(env []string, name string, args ...string) error {
	if err := paths.EnsureDir(paths.MustRoot()); err != nil {
		return err
	}
	logFile, err := os.OpenFile(paths.Log(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, paths.FileMode)
	if err != nil {
		return err
	}
	defer logFile.Close()

	cmd := exec.Command(name, args...)
	cmd.Env = env
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func SpawnRecycle(deviceName string) error { return spawnSelf("device", "recycle", deviceName) }

func SpawnRestore(deviceID string) error { return spawnSelf("device", "restore", deviceID) }

// spawnSelf runs the current binary so a worker always matches the caller's
// version.
func spawnSelf(args ...string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	return spawnDetached(nil, self, args...)
}
