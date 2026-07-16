package paths

import (
	"os"
	"path/filepath"
)

const (
	DirMode  os.FileMode = 0o700
	FileMode os.FileMode = 0o600
)

func Root() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".atelier"), nil
}

func MustRoot() string {
	p, err := Root()
	if err != nil {
		panic("atelierd: cannot resolve ~/.atelier: " + err.Error())
	}
	return p
}

func EnsureDir(dir string) error {
	return os.MkdirAll(dir, DirMode)
}

func Outbox() string { return filepath.Join(MustRoot(), "outbox") }

func SessionTitles() string { return filepath.Join(MustRoot(), "session-titles") }

func ClaudeDesktopSessionStore() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Application Support", "Claude", "claude-code-sessions"), nil
}

func Credentials() string { return filepath.Join(MustRoot(), "credentials") }

func Status() string { return filepath.Join(MustRoot(), "status") }

func Log() string { return filepath.Join(MustRoot(), "atelierd.log") }

func OutboxFile(ulid string) string { return filepath.Join(Outbox(), ulid+".json") }

func Devices() string { return filepath.Join(MustRoot(), "devices.json") }

func DevicesLock() string { return filepath.Join(MustRoot(), "devices.lock") }

func Forge() string { return filepath.Join(MustRoot(), "forge") }

func ForgeLock() string { return filepath.Join(Forge(), "forge.lock") }

func ForgeRun(runID string) string { return filepath.Join(Forge(), runID) }

func ForgeRunState(runID string) string { return filepath.Join(ForgeRun(runID), "run.json") }

func ForgeRunLock(runID string) string { return filepath.Join(ForgeRun(runID), "run.lock") }

func ForgeCampaign(runID string) string { return filepath.Join(ForgeRun(runID), "campaign.json") }

func ForgeLedger(runID string) string { return filepath.Join(ForgeRun(runID), "ledger.json") }

func ForgeCaptures(runID string) string { return filepath.Join(ForgeRun(runID), "captures") }

func ForgePassCaptures(runID, passID string) string {
	return filepath.Join(ForgeCaptures(runID), passID)
}
