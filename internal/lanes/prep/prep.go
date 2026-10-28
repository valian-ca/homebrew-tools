package prep

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/valian-ca/homebrew-tools/internal/lanes/config"
	"github.com/valian-ca/homebrew-tools/internal/lanes/fsx"
	"github.com/valian-ca/homebrew-tools/internal/lanes/gitx"
	"github.com/valian-ca/homebrew-tools/internal/lanes/paths"
)

const IncludeFile = ".worktreeinclude"

type Options struct {
	Worktree gitx.Worktree
	MainRoot string
	Config   *config.Config
	Environ  []string
	Log      io.Writer
}

// Run brings the worktree up to date: the built-in copies first, then every
// declared step whose trigger changed since it last succeeded.
func Run(ctx context.Context, o Options) error {
	if o.Log == nil {
		o.Log = io.Discard
	}
	return fsx.WithLock(paths.PrepLock(o.Worktree.AdminDir), func() error {
		if err := CopyIncludes(o.MainRoot, o.Worktree.Root, o.Log); err != nil {
			return err
		}
		if err := CreateFromTemplates(ctx, o.Worktree.Root, o.Log); err != nil {
			return err
		}
		for _, s := range o.Config.Setup {
			if err := runStep(ctx, o, s); err != nil {
				return err
			}
		}
		return nil
	})
}

// CopyIncludes copies from the main checkout each file .worktreeinclude
// names that the worktree lacks. The manifest is branch content, so a path
// leaving the tree or a symlink is refused rather than followed.
func CopyIncludes(mainRoot, root string, log io.Writer) error {
	data, err := os.ReadFile(filepath.Join(root, IncludeFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var includes, excludes []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
		case strings.HasPrefix(line, "!"):
			excludes = append(excludes, line[1:])
		default:
			includes = append(includes, line)
		}
	}
	for _, pattern := range includes {
		if !contained(pattern) {
			fmt.Fprintf(log, "lanes: skipping %s pattern outside the tree: %s\n", IncludeFile, pattern)
			continue
		}
		matches, err := filepath.Glob(filepath.Join(mainRoot, pattern))
		if err != nil {
			return fmt.Errorf("%s: %q: %w", IncludeFile, pattern, err)
		}
		for _, src := range matches {
			rel, err := filepath.Rel(mainRoot, src)
			if err != nil || !contained(rel) || excluded(rel, excludes) {
				continue
			}
			placed, err := place(src, filepath.Join(root, rel))
			if err != nil {
				return err
			}
			if placed {
				fmt.Fprintf(log, "lanes: copied %s\n", rel)
			}
		}
	}
	return nil
}

func CreateFromTemplates(ctx context.Context, root string, log io.Writer) error {
	files, err := gitx.TrackedMatching(ctx, root, "**/*.template")
	if err != nil {
		return err
	}
	for _, tmpl := range files {
		base := path.Base(tmpl)
		if !strings.HasPrefix(base, ".env") {
			continue
		}
		local := strings.TrimSuffix(tmpl, ".template")
		placed, err := place(filepath.Join(root, tmpl), filepath.Join(root, local))
		if err != nil {
			return err
		}
		if placed {
			fmt.Fprintf(log, "lanes: created %s from %s\n", local, base)
		}
	}
	return nil
}

func contained(p string) bool {
	if p == "" || filepath.IsAbs(p) {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(p), "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

func excluded(rel string, excludes []string) bool {
	for _, x := range excludes {
		if ok, _ := path.Match(x, filepath.ToSlash(rel)); ok {
			return true
		}
	}
	return false
}

// place copies src to dst unless dst exists in any form, a dangling symlink
// included: nothing already in the worktree is ever overwritten.
func place(src, dst string) (bool, error) {
	if _, err := os.Lstat(dst); err == nil {
		return false, nil
	}
	info, err := os.Lstat(src)
	if err != nil || !info.Mode().IsRegular() {
		return false, nil
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return false, err
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return false, err
	}
	return true, f.Close()
}

type StepError struct {
	Step string
	Tail string
	Err  error
}

func (e *StepError) Error() string {
	return fmt.Sprintf("prep step %s failed: %v\n%s", e.Step, e.Err, e.Tail)
}

func runStep(ctx context.Context, o Options, s config.Step) error {
	stampPath := paths.PrepStamp(o.Worktree.AdminDir, s.Name)
	want, err := stamp(o.Worktree.Root, s)
	if err != nil {
		return err
	}
	if cur, err := os.ReadFile(stampPath); err == nil && string(cur) == want {
		return nil
	}
	fmt.Fprintf(o.Log, "lanes: running prep step %s\n", s.Name)
	c := exec.CommandContext(ctx, "/bin/sh", "-c", s.Run)
	c.Dir = o.Worktree.Root
	c.Env = o.Environ
	var out bytes.Buffer
	c.Stdout = io.MultiWriter(&out, o.Log)
	c.Stderr = c.Stdout
	if err := c.Run(); err != nil {
		return &StepError{Step: s.Name, Tail: Tail(out.String(), 40), Err: err}
	}
	return fsx.WriteAtomic(stampPath, []byte(want), paths.FileMode)
}

// stamp fingerprints a step's triggers. A step without triggers gets a
// constant stamp, so it runs once per worktree.
func stamp(root string, s config.Step) (string, error) {
	h := sha256.New()
	for _, f := range s.WhenChanged {
		data, err := os.ReadFile(filepath.Join(root, f))
		switch {
		case errors.Is(err, os.ErrNotExist):
			fmt.Fprintf(h, "missing:%s\n", f)
		case err != nil:
			return "", err
		default:
			sum := sha256.Sum256(data)
			fmt.Fprintf(h, "file:%s:%x\n", f, sum)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func Tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
