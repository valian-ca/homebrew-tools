package cli

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/valian-ca/homebrew-tools/internal/lanes/client"
	"github.com/valian-ca/homebrew-tools/internal/lanes/config"
	"github.com/valian-ca/homebrew-tools/internal/lanes/env"
	"github.com/valian-ca/homebrew-tools/internal/lanes/gitx"
	"github.com/valian-ca/homebrew-tools/internal/lanes/lane"
	"github.com/valian-ca/homebrew-tools/internal/lanes/ports"
	"github.com/valian-ca/homebrew-tools/internal/lanes/prep"
	"github.com/valian-ca/homebrew-tools/internal/lanes/proto"
	"github.com/valian-ca/homebrew-tools/internal/lanes/workspace"
)

var commands = []string{"lanes start", "lanes stop", "lanes logs <piece>", "lanes restart <piece>", "lanes test-env -- <command>", "lanes list", "lanes help contract"}

func demoWorkspace(names ...string) *workspace.Workspace {
	return &workspace.Workspace{
		WT:    gitx.Worktree{Root: "/code/acme/.claude/worktrees/val-374", AdminDir: "/code/acme/.git/worktrees/val-374"},
		Cfg:   &config.Config{Ports: config.Ports{Block: 20, Names: names}},
		Lane:  lane.Record{ID: "feature_val_374_discov_3f9a1c"},
		Block: ports.Entry{Start: 20000, Size: 20},
		Vars:  env.Vars{{Name: "LANES_HOST", Value: "127.0.0.1"}},
	}
}

func TestLaneContext(t *testing.T) {
	w := demoWorkspace("web", "extra", "auth", "firestore", "test_auth", "test_firestore", "hub")
	got := laneContext(w, stackNeverStarted)
	lines := strings.Split(got, "\n")
	if len(lines) > 12 {
		t.Fatalf("the block holds %d lines, 12 at most:\n%s", len(lines), got)
	}
	want := []string{
		"lanes: this session works in lane feature_val_374_discov_3f9a1c (worktree /code/acme/.claude/worktrees/val-374), host 127.0.0.1.",
		"Stack: never started. Run `lanes start` before using the ports.",
		"Ports: web=20000 extra=20001 auth=20002 firestore=20003 hub=20006",
	}
	for i, l := range want {
		if lines[i] != l {
			t.Errorf("line %d = %q, want %q", i+1, lines[i], l)
		}
	}
	for _, c := range commands {
		if !strings.Contains(lines[len(lines)-1], c) {
			t.Errorf("the commands line lacks %s: %q", c, lines[len(lines)-1])
		}
	}
	if strings.Contains(got, "test_") {
		t.Errorf("the block names a test_* port:\n%s", got)
	}
}

func TestLaneContextWithoutPortsOrWithProblems(t *testing.T) {
	got := laneContext(demoWorkspace(), stackRunning)
	if strings.Contains(got, "Ports:") {
		t.Errorf("a project without named ports got a Ports line:\n%s", got)
	}
	got = laneContext(demoWorkspace("web"), stackRunning, nil, errors.New("setup step deps failed:\nexit status 1"), errors.New("env.X: bad"))
	if !strings.Contains(got, "\nProblem: setup step deps failed: exit status 1\nProblem: env.X: bad\n") {
		t.Errorf("problems are not one line each:\n%s", got)
	}
	if n := strings.Count(got, "\n") + 1; n > 12 {
		t.Errorf("%d lines", n)
	}
}

func TestPrepFailureStaysOneShortLine(t *testing.T) {
	got := laneContext(demoWorkspace("web"), stackRunning, notExported(problem(&prep.StepError{Step: "deps", Err: errors.New("exit status 1"), Tail: "npm ERR! a\nnpm ERR! b\nnpm ERR! c"})))
	if !strings.Contains(got, "Problem: prep step deps failed: exit status 1") {
		t.Errorf("the prep failure is not named in one line:\n%s", got)
	}
	if strings.Contains(got, "npm ERR!") {
		t.Errorf("the step's output reached the context:\n%s", got)
	}
}

func TestStackLine(t *testing.T) {
	const admin = "/code/acme/.git/worktrees/val-374"
	running := proto.Response{Stacks: []proto.StackStatus{{AdminDir: "/other", Running: true}, {AdminDir: admin, Running: true}}}
	idle := proto.Response{Stacks: []proto.StackStatus{{AdminDir: admin, Running: false}}}
	cases := []struct {
		name    string
		resp    proto.Response
		err     error
		started bool
		want    string
	}{
		{"running", running, nil, true, stackRunning},
		{"stopped", idle, nil, true, stackStopped},
		{"never started", proto.Response{}, nil, false, stackNeverStarted},
		{"daemon down", proto.Response{}, client.ErrDaemonDown, true, stackDaemonDown},
	}
	for _, c := range cases {
		if got := stackLine(c.resp, c.err, admin, c.started); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	if got := stackLine(proto.Response{}, fmt.Errorf("context deadline exceeded"), admin, false); !strings.HasPrefix(got, "Stack: unknown") {
		t.Errorf("a silent lanesd gives %q", got)
	}
	if !strings.Contains(stackDaemonDown, "brew services start lanes") {
		t.Errorf("the unknown state does not say how to start lanesd: %q", stackDaemonDown)
	}
}

func TestUnsettledContext(t *testing.T) {
	err := errors.New("/code/acme/.claude/worktrees/val-374/lanes.yaml: yaml: unmarshal errors:\n  line 7: field portz not found in type config.Config")
	got := unsettledContext("/code/acme/.claude/worktrees/val-374", "feature_val_374_discov_3f9a1c", err)
	lines := strings.Split(got, "\n")
	if len(lines) > 12 {
		t.Fatalf("%d lines", len(lines))
	}
	if !strings.Contains(lines[0], "(last lane feature_val_374_discov_3f9a1c)") {
		t.Errorf("the recorded lane is missing: %q", lines[0])
	}
	if lines[1] != "Error: /code/acme/.claude/worktrees/val-374/lanes.yaml: yaml: unmarshal errors: line 7: field portz not found in type config.Config" {
		t.Errorf("the error is not on one line: %q", lines[1])
	}
	if !strings.Contains(lines[len(lines)-1], "lanes help contract") {
		t.Errorf("no commands line: %q", lines[len(lines)-1])
	}
	if strings.Contains(unsettledContext("/x", "", err), "last lane") {
		t.Error("a worktree without a recorded lane names one")
	}
}

func TestMainCheckoutNote(t *testing.T) {
	if n := strings.Count(mainCheckoutNote, "\n") + 1; n != 2 {
		t.Fatalf("the note holds %d lines, want 2", n)
	}
	if !strings.Contains(mainCheckoutNote, "lanes help contract") || !strings.Contains(mainCheckoutNote, "main checkout") {
		t.Errorf("note: %q", mainCheckoutNote)
	}
}

func TestContextEvent(t *testing.T) {
	cases := []struct {
		hook, event, want string
	}{
		{"session-start", "SessionStart", "SessionStart"},
		{"session-start", "", "SessionStart"},
		{"cwd-changed", "PostToolUse", "PostToolUse"},
		{"cwd-changed", "CwdChanged", ""},
		{"cwd-changed", "", ""},
	}
	for _, c := range cases {
		if got := contextEvent(c.hook, hookPayload{HookEventName: c.event}); got != c.want {
			t.Errorf("%s under %q: %q, want %q", c.hook, c.event, got, c.want)
		}
	}
}
