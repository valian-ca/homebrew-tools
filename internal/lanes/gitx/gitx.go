package gitx

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

var ErrNotRepo = errors.New("not inside a git repository")

type Worktree struct {
	Root      string
	AdminDir  string
	CommonDir string
}

// IsMain reports the main checkout: only there does git's per-worktree admin
// dir coincide with the shared one.
func (w Worktree) IsMain() bool { return w.AdminDir == w.CommonDir }

func Detect(ctx context.Context, dir string) (Worktree, error) {
	out, err := run(ctx, dir, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-dir", "--git-common-dir")
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return Worktree{}, ErrNotRepo
		}
		return Worktree{}, err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		return Worktree{}, fmt.Errorf("git rev-parse: unexpected output %q", out)
	}
	return Worktree{
		Root:      realpath(lines[0]),
		AdminDir:  realpath(lines[1]),
		CommonDir: realpath(lines[2]),
	}, nil
}

// Gone reports a linked worktree git no longer knows: its admin dir lost its
// gitdir file, the mark git itself prunes on. The dir alone proves nothing,
// since Claude Code removes git's files and leaves lanes' own beside them.
func Gone(adminDir string) (bool, error) {
	_, err := os.Stat(filepath.Join(adminDir, "gitdir"))
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	return false, err
}

func realpath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// MainRoot is the main checkout's working tree, which git always lists first.
func MainRoot(ctx context.Context, dir string) (string, error) {
	out, err := run(ctx, dir, "worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		if p, ok := strings.CutPrefix(sc.Text(), "worktree "); ok {
			return realpath(p), nil
		}
	}
	return "", errors.New("git worktree list: no main worktree")
}

// Branch is "" on a detached HEAD.
func Branch(ctx context.Context, dir string) (string, error) {
	out, err := run(ctx, dir, "symbolic-ref", "-q", "--short", "HEAD")
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func Head(ctx context.Context, dir string) (string, error) {
	out, err := run(ctx, dir, "rev-parse", "-q", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", nil
	}
	return strings.TrimSpace(out), nil
}

func refExists(ctx context.Context, dir, ref string) bool {
	_, err := run(ctx, dir, "rev-parse", "-q", "--verify", ref+"^{commit}")
	return err == nil
}

// DefaultBranch resolves the branch merged work lands on, preferring the
// remote's view: a local main can lag far behind origin.
func DefaultBranch(ctx context.Context, dir string) string {
	if out, err := run(ctx, dir, "symbolic-ref", "-q", "refs/remotes/origin/HEAD"); err == nil {
		if ref := strings.TrimSpace(out); ref != "" && refExists(ctx, dir, ref) {
			return ref
		}
	}
	for _, ref := range []string{"refs/remotes/origin/main", "refs/remotes/origin/master", "refs/heads/main", "refs/heads/master"} {
		if refExists(ctx, dir, ref) {
			return ref
		}
	}
	return ""
}

func CommitsOutside(ctx context.Context, dir, tip, base string) (int, error) {
	out, err := run(ctx, dir, "rev-list", "--count", base+".."+tip)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}

type Rename struct{ From, To string }

// Renames reads HEAD's reflog and the branch's own, oldest first.
func Renames(ctx context.Context, dir, branch string) []Rename {
	var renames []Rename
	seen := map[Rename]bool{}
	for _, ref := range []string{"HEAD", "refs/heads/" + branch} {
		out, err := run(ctx, dir, "reflog", "show", "--format=%gs", ref, "--")
		if err != nil {
			continue
		}
		lines := strings.Split(strings.TrimSpace(out), "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			r, ok := parseRename(lines[i])
			if ok && !seen[r] {
				seen[r] = true
				renames = append(renames, r)
			}
		}
	}
	return renames
}

func parseRename(subject string) (Rename, bool) {
	rest, ok := strings.CutPrefix(subject, "Branch: renamed refs/heads/")
	if !ok {
		return Rename{}, false
	}
	from, to, ok := strings.Cut(rest, " to refs/heads/")
	if !ok {
		return Rename{}, false
	}
	return Rename{From: from, To: to}, true
}

func RenamedInto(renames []Rename, from, to string) bool {
	reach := map[string]bool{from: true}
	for changed := true; changed; {
		changed = false
		for _, r := range renames {
			if reach[r.From] && !reach[r.To] {
				reach[r.To] = true
				changed = true
			}
		}
	}
	return reach[to]
}

func IsTracked(ctx context.Context, root, path string) bool {
	_, err := run(ctx, root, "ls-files", "--error-unmatch", "--", path)
	return err == nil
}

func IsIgnored(ctx context.Context, root, path string) bool {
	_, err := run(ctx, root, "check-ignore", "-q", "--no-index", "--", path)
	return err == nil
}

func TrackedMatching(ctx context.Context, root, glob string) ([]string, error) {
	out, err := run(ctx, root, "ls-files", "-z", "--", ":(glob)"+glob)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(out, "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

func run(ctx context.Context, dir string, args ...string) (string, error) {
	c := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitErr.Stderr = stderr.Bytes()
		}
		return string(out), err
	}
	return string(out), nil
}
