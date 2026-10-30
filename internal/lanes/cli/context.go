package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/lanes/client"
	"github.com/valian-ca/homebrew-tools/internal/lanes/config"
	"github.com/valian-ca/homebrew-tools/internal/lanes/firebase"
	"github.com/valian-ca/homebrew-tools/internal/lanes/gitx"
	"github.com/valian-ca/homebrew-tools/internal/lanes/lane"
	"github.com/valian-ca/homebrew-tools/internal/lanes/proto"
	"github.com/valian-ca/homebrew-tools/internal/lanes/workspace"
)

const stateBudget = time.Second

const laneCommands = "Commands: lanes start · lanes stop · lanes logs <piece> · lanes restart <piece> · lanes test-env -- <command> · lanes list · lanes help contract (the full contract)"

const mainCheckoutNote = "lanes: this project gives each worktree its own stack; this session is on the main checkout, which has no lane — use the project's own commands here.\n" +
	"Run `lanes help contract` for the contract: ports, TMPDIR and stack per worktree."

const (
	stackRunning      = "Stack: running."
	stackStopped      = "Stack: stopped. Run `lanes start` before using the ports."
	stackNeverStarted = "Stack: never started. Run `lanes start` before using the ports."
	stackDaemonDown   = "Stack: unknown — lanesd is not running (brew services start lanes)."
)

func laneContext(w *workspace.Workspace, stack string, problems ...error) string {
	var b strings.Builder
	fmt.Fprintf(&b, "lanes: this session works in lane %s (worktree %s), host %s.\n", w.Lane.ID, w.WT.Root, w.Vars.Map()["LANES_HOST"])
	b.WriteString(stack + "\n")
	var named []string
	for i, n := range w.Cfg.Ports.Names {
		if !strings.HasPrefix(n, firebase.TestPrefix) {
			named = append(named, fmt.Sprintf("%s=%d", n, w.Block.Start+i))
		}
	}
	if len(named) > 0 {
		b.WriteString("Ports: " + strings.Join(named, " ") + "\n")
	}
	for _, p := range problems {
		if p != nil {
			b.WriteString("Problem: " + oneLine(p) + "\n")
		}
	}
	b.WriteString(laneCommands)
	return b.String()
}

func unsettledContext(root, laneID string, err error) string {
	last := ""
	if laneID != "" {
		last = " (last lane " + laneID + ")"
	}
	return fmt.Sprintf("lanes: this session's worktree %s has no settled lane%s — its variables are not exported.\n"+
		"Error: %s\n"+
		"Fix it, then run `lanes start` or open a new session.\n%s", root, last, oneLine(err), laneCommands)
}

func stackLine(resp proto.Response, err error, adminDir string, started bool) string {
	switch {
	case errors.Is(err, client.ErrDaemonDown):
		return stackDaemonDown
	case err != nil:
		return "Stack: unknown — lanesd did not answer: " + oneLine(err) + "."
	}
	for _, s := range resp.Stacks {
		if s.AdminDir == adminDir && s.Running {
			return stackRunning
		}
	}
	if started {
		return stackStopped
	}
	return stackNeverStarted
}

func stackState(ctx context.Context, w *workspace.Workspace) string {
	ctx, cancel := context.WithTimeout(ctx, stateBudget)
	defer cancel()
	resp, err := client.Call(ctx, proto.Request{Op: proto.OpList})
	return stackLine(resp, err, w.WT.AdminDir, w.Lane.StartedAt != nil)
}

func quietContext(ctx context.Context, dir string, err error) string {
	if !errors.Is(err, workspace.ErrMainCheckout) {
		return ""
	}
	wt, derr := gitx.Detect(ctx, dir)
	if derr != nil {
		return ""
	}
	if _, serr := os.Stat(filepath.Join(wt.Root, config.FileName)); serr != nil {
		return ""
	}
	return mainCheckoutNote
}

func unsettledContextFor(ctx context.Context, dir string, err error) string {
	wt, derr := gitx.Detect(ctx, dir)
	if derr != nil {
		return unsettledContext(dir, "", err)
	}
	laneID := ""
	if rec, _ := lane.Load(wt.AdminDir); rec != nil {
		laneID = rec.ID
	}
	return unsettledContext(wt.Root, laneID, err)
}

func oneLine(err error) string {
	return strings.Join(strings.Fields(err.Error()), " ")
}

type hookOutput struct {
	HookSpecificOutput hookContext `json:"hookSpecificOutput"`
}

type hookContext struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

func writeContext(out io.Writer, event, text string) error {
	return json.NewEncoder(out).Encode(hookOutput{hookContext{HookEventName: event, AdditionalContext: text}})
}
