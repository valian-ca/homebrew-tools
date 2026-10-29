package tmpdir

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/valian-ca/homebrew-tools/internal/lanes/paths"
)

// Path is short and independent of the worktree's location: unix socket
// paths are capped at 104 bytes on macOS, and emulators nest sockets deep
// under TMPDIR.
func Path(commonDir, adminDir string) string {
	sum := sha256.Sum256([]byte(commonDir + "\x00" + adminDir))
	base := "/tmp"
	if v, ok := paths.TestOverride("LANES_TMPDIR_ROOT"); ok {
		base = v
	}
	return filepath.Join(base, fmt.Sprintf("lanes-%d-%s", os.Getuid(), hex.EncodeToString(sum[:4])))
}

func Ensure(dir string) error {
	if err := os.MkdirAll(paths.Logs(dir), paths.DirMode); err != nil {
		return err
	}
	return os.Chmod(dir, paths.DirMode)
}
