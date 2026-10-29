package generate

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/template"

	"github.com/valian-ca/homebrew-tools/internal/lanes/config"
	"github.com/valian-ca/homebrew-tools/internal/lanes/fsx"
	"github.com/valian-ca/homebrew-tools/internal/lanes/gitx"
)

func Run(ctx context.Context, root string, specs []config.Generate, vars map[string]string, log io.Writer) error {
	for _, g := range specs {
		out := g.Output()
		if gitx.IsTracked(ctx, root, out) {
			return fmt.Errorf("generate: %s is tracked by git; lanes never writes a tracked file — untrack it and add it to .gitignore", out)
		}
		src, err := os.ReadFile(filepath.Join(root, g.Template))
		if err != nil {
			return fmt.Errorf("generate: %w", err)
		}
		t, err := template.New(g.Template).Option("missingkey=error").Parse(string(src))
		if err != nil {
			return fmt.Errorf("generate: %w", err)
		}
		var buf bytes.Buffer
		if err := t.Execute(&buf, vars); err != nil {
			return fmt.Errorf("generate: %w", err)
		}
		dst := filepath.Join(root, out)
		mode := os.FileMode(0o644)
		if info, err := os.Stat(filepath.Join(root, g.Template)); err == nil {
			mode = info.Mode().Perm()
		}
		changed, err := fsx.WriteIfChanged(dst, buf.Bytes(), mode)
		if err != nil {
			return fmt.Errorf("generate: %w", err)
		}
		if changed && !gitx.IsIgnored(ctx, root, out) {
			fmt.Fprintf(log, "lanes: warning: %s is not ignored by git — add it to .gitignore\n", out)
		}
	}
	return nil
}
