package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/lanes/paths"
	"github.com/valian-ca/homebrew-tools/internal/lanes/prep"
)

func showLog(ctx context.Context, out io.Writer, tmp, piece string, lines int, follow bool) error {
	path := paths.PieceLog(tmp, piece)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return errorf("no log for %q in %s%s", piece, paths.Logs(tmp), available(tmp))
	}
	if err != nil {
		return err
	}
	if tail := prep.Tail(string(data), lines); tail != "" {
		printf(out, "%s\n", tail)
	}
	if !follow {
		return nil
	}
	return followLog(ctx, out, path, int64(len(data)))
}

// A file that shrank was truncated by a restart, and is read again from its
// start.
func followLog(ctx context.Context, out io.Writer, path string, offset int64) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(250 * time.Millisecond):
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.Size() < offset {
			offset = 0
		}
		if info.Size() == offset {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		if _, err := f.Seek(offset, io.SeekStart); err == nil {
			n, _ := io.Copy(out, f)
			offset += n
		}
		f.Close()
	}
}

func available(tmp string) string {
	entries, err := os.ReadDir(paths.Logs(tmp))
	if err != nil {
		return ""
	}
	var names []string
	for _, e := range entries {
		if n, ok := strings.CutSuffix(e.Name(), ".log"); ok && n != "process-compose" {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	return " — logs exist for: " + strings.Join(names, ", ")
}
