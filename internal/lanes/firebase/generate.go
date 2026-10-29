package firebase

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/valian-ca/homebrew-tools/internal/lanes/config"
	"github.com/valian-ca/homebrew-tools/internal/lanes/fsx"
	"github.com/valian-ca/homebrew-tools/internal/lanes/gitx"
)

// Generate writes the stack's and the test series' configs beside the base
// config, where firebase-tools resolves the functions source and the rules.
// A failure removes both, so no stale config outlives a broken base; a test
// series short of ports only loses its own, since `lanes test-env` names them.
func Generate(ctx context.Context, root string, cfg *config.Config, blockStart int, log io.Writer) error {
	fb := cfg.Firebase()
	if fb == nil {
		return nil
	}
	stackPath, testPath := ConfigPath(root, fb, Stack), ConfigPath(root, fb, Test)
	err := generate(ctx, root, cfg, fb, blockStart, log)
	if err != nil {
		removeAll(stackPath, testPath)
	}
	return err
}

func generate(ctx context.Context, root string, cfg *config.Config, fb *config.Firebase, blockStart int, log io.Writer) error {
	for _, s := range []Series{Stack, Test} {
		if rel := relTo(root, ConfigPath(root, fb, s)); gitx.IsTracked(ctx, root, rel) {
			return fmt.Errorf("firebase: %s is tracked by git; lanes never writes a tracked file — untrack it and add it to .gitignore", rel)
		}
	}
	b, err := LoadBase(root, fb)
	if err != nil {
		return err
	}
	ports := Ports(cfg.Ports.Names, blockStart)
	for _, s := range []Series{Stack, Test} {
		path := ConfigPath(root, fb, s)
		data, err := Render(b, NewPlan(b, fb, s), ports)
		if err != nil {
			if s == Test {
				removeAll(path)
				continue
			}
			return fmt.Errorf("firebase: %w", err)
		}
		changed, err := fsx.WriteIfChanged(path, data, 0o644)
		if err != nil {
			return fmt.Errorf("firebase: %w", err)
		}
		if rel := relTo(root, path); changed && !gitx.IsIgnored(ctx, root, rel) {
			fmt.Fprintf(log, "lanes: warning: %s is not ignored by git — add it to .gitignore\n", rel)
		}
	}
	return nil
}

func removeAll(paths ...string) {
	for _, p := range paths {
		_ = os.Remove(p)
	}
}

func relTo(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return rel
}
