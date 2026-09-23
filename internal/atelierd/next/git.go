package next

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > MaxFileBytes {
		return 0, fmt.Errorf("git output exceeds limit")
	}
	return b.Buffer.Write(p)
}

func git(ctx context.Context, cwd string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	prefix := []string{"--no-replace-objects", "--no-optional-locks", "-C", cwd}
	cmd := exec.CommandContext(ctx, "git", append(prefix, args...)...)
	cmd.WaitDelay = time.Second
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		switch key {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE", "GIT_SHALLOW_FILE", "GIT_REPLACE_REF_BASE":
			continue
		}
		cmd.Env = append(cmd.Env, value)
	}
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr boundedOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSuffix(stdout.String(), "\n"), nil
}

func inspectCheckout(ctx context.Context, cwd string) (Checkout, string, error) {
	var c Checkout
	root, err := git(ctx, cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return c, "", fmt.Errorf("%w: %v", ErrCheckout, err)
	}
	c.Worktree, err = filepath.EvalSymlinks(root)
	if err != nil {
		return c, "", fmt.Errorf("%w: %v", ErrCheckout, err)
	}
	common, err := git(ctx, cwd, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return c, "", fmt.Errorf("%w: %v", ErrCheckout, err)
	}
	c.Repository, err = filepath.EvalSymlinks(common)
	if err != nil {
		return c, "", fmt.Errorf("%w: %v", ErrCheckout, err)
	}
	c.Branch, err = git(ctx, cwd, "symbolic-ref", "--quiet", "HEAD")
	if err != nil || !strings.HasPrefix(c.Branch, "refs/heads/") {
		return c, "", fmt.Errorf("%w: attached branch required", ErrCheckout)
	}
	head, err := git(ctx, cwd, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || !hashPattern.MatchString(head) {
		return c, "", fmt.Errorf("%w: existing commit required", ErrCheckout)
	}
	return c, head, nil
}

func cleanCheckout(ctx context.Context, cwd string) error {
	status, err := git(ctx, cwd, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return err
	}
	if status != "" {
		return fmt.Errorf("%w: clean worktree and index required", ErrCheckout)
	}
	return nil
}

func stagedTree(ctx context.Context, cwd string) (string, error) {
	unstaged, err := git(ctx, cwd, "diff", "--name-only", "-z", "--ignore-submodules=none")
	if err != nil {
		return "", err
	}
	untracked, err := git(ctx, cwd, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", err
	}
	if unstaged != "" || untracked != "" {
		return "", fmt.Errorf("%w: stage all contribution changes before preparing integration", ErrInvalid)
	}
	tree, err := git(ctx, cwd, "write-tree")
	if err != nil {
		return "", err
	}
	if !hashPattern.MatchString(tree) {
		return "", fmt.Errorf("%w: invalid staged tree", ErrInvalid)
	}
	return tree, nil
}

func proveIntegration(ctx context.Context, cwd, head string, i *Integration) error {
	if head == i.Parent {
		return fmt.Errorf("%w: no commit yet; commit the prepared tree", ErrReconcile)
	}
	value, err := git(ctx, cwd, "show", "-s", "--format=%P%n%T", head)
	if err != nil {
		return err
	}
	parts := strings.Split(value, "\n")
	if len(parts) != 2 || parts[0] != i.Parent || parts[1] != i.Tree {
		return fmt.Errorf("%w: HEAD must be one non-merge commit with the prepared parent and tree", ErrReconcile)
	}
	return cleanCheckout(ctx, cwd)
}
