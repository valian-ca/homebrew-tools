package cmds

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/next"
)

func executeNext(args ...string) (string, string, error) {
	command := NewNextCmd()
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs(args)
	command.SilenceErrors, command.SilenceUsage = true, true
	err := command.Execute()
	return stdout.String(), stderr.String(), err
}

func TestNextContractAndHelp(t *testing.T) {
	out, stderr, err := executeNext("contract")
	if err != nil || out != "3\n" || stderr != "" {
		t.Fatal(out, stderr, err)
	}
	out, _, err = executeNext("--help")
	if err != nil || !strings.Contains(out, "~/.atelier-next") || !strings.Contains(out, "HOME") || !strings.Contains(out, "cannot be set arbitrarily") {
		t.Fatal(out, err)
	}
	if out, _, err := executeNext("delivery", "teleport"); err == nil || out != "" {
		t.Fatal("unimplemented delivery must fail", out, err)
	}
}

func TestNextExitCodes(t *testing.T) {
	for _, test := range []struct {
		err  error
		code int
	}{
		{next.ErrNotFound, 30}, {next.ErrAmbiguous, 31}, {next.ErrConflict, 32}, {next.ErrLease, 33},
		{next.ErrCheckout, 34}, {next.ErrInvalid, 35}, {next.ErrReconcile, 36}, {next.ErrBusy, 37},
	} {
		if got := ExitCode(fmt.Errorf("wrapped: %w", test.err)); got != test.code {
			t.Fatalf("%v: %d", test.err, got)
		}
	}
}

func TestNextCLIFullLifecycleAndLegacyIsolation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR"} {
		t.Setenv(key, "")
	}
	repo := filepath.Join(home, "repo")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	runGit := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cmd := exec.CommandContext(ctx, "git", args...)
		// Empty GIT_DIR is not the same as unset to git.
		for _, env := range os.Environ() {
			if !strings.HasPrefix(env, "GIT_DIR=") && !strings.HasPrefix(env, "GIT_WORK_TREE=") && !strings.HasPrefix(env, "GIT_INDEX_FILE=") && !strings.HasPrefix(env, "GIT_COMMON_DIR=") {
				cmd.Env = append(cmd.Env, env)
			}
		}
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatal(string(out), err)
		}
		return strings.TrimSpace(string(out))
	}
	for _, args := range [][]string{
		{"init", "-b", "feature"}, {"config", "user.name", "CLI Test"}, {"config", "user.email", "cli@example.invalid"},
		{"config", "commit.gpgsign", "false"}, {"commit", "--allow-empty", "-m", "test: base"},
	} {
		runGit(args...)
	}
	startFile := commandTestFile(t, "start.json", `{"root":{"id":"11111111-1111-4111-8111-111111111111","identifier":"TEST-1"},"mode":"standalone"}`)
	args := []string{"campaign", "start", "--from", startFile, "--session", "session-a", "--operation", "start-1"}
	out, stderr, err := executeNext(args...)
	if err != nil || stderr != "" {
		t.Fatal(out, stderr, err)
	}
	var receipt next.Receipt
	if err := json.Unmarshal([]byte(out), &receipt); err != nil || receipt.Token == "" {
		t.Fatal(out, err)
	}
	if replay, _, err := executeNext(args...); err != nil || replay != out {
		t.Fatal(replay, err)
	}
	flags := []string{"--campaign", receipt.CampaignID, "--session", "session-a", "--token", receipt.Token}
	invoke := func(command []string, rest ...string) string {
		t.Helper()
		argv := append(append(append([]string{}, command...), flags...), rest...)
		out, stderr, err := executeNext(argv...)
		if err != nil || stderr != "" || !json.Valid([]byte(out)) {
			t.Fatal(argv, out, stderr, err)
		}
		return out
	}
	plan := commandTestFile(t, "plan.json", `{"reference":"document-1","revision":1,"contributions":[{"ticket":{"id":"11111111-1111-4111-8111-111111111111","identifier":"TEST-1"},"contract":"Agreed behavior","dependsOn":[],"requiredTests":["static"]}],"finalChecks":{"criteria":["AC1"],"tests":["static"],"surfaces":[],"visual":false,"noVisualReason":"Static fixture"}}`)
	invoke([]string{"plan", "set"}, "--operation", "plan-1", "--from", plan)
	invoke([]string{"campaign", "block"}, "--operation", "block-1", "--class", "environment", "--reason", "missing dependency")
	invoke([]string{"campaign", "resume"}, "--operation", "resume-1", "--reason", "dependency available")
	invoke([]string{"contribution", "open"}, "--operation", "open-1", "--ticket", "TEST-1")
	out, _, err = executeNext("campaign", "status", "--campaign", receipt.CampaignID)
	if err != nil || strings.Contains(out, "operations") || strings.Contains(out, "events") {
		t.Fatal(out, err)
	}
	var status next.Campaign
	if err := json.Unmarshal([]byte(out), &status); err != nil || status.State != "realizing" {
		t.Fatal(out, err)
	}
	out, _, err = executeNext("events", "--campaign", receipt.CampaignID)
	var events []next.Event
	if err != nil || json.Unmarshal([]byte(out), &events) != nil || len(events) != 5 {
		t.Fatal(out, err)
	}
	out, _, err = executeNext("campaign", "find", "--ticket", "TEST-1")
	if err != nil || out != receipt.CampaignID+"\n" {
		t.Fatal(out, err)
	}
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit("add", "feature.txt")
	evidence := next.Evidence{Tree: runGit("write-tree"), Tests: []next.TestEvidence{{Name: "static", Command: "fixture-check", Status: "pass", Reference: "fixture-log"}}, Review: next.ReviewEvidence{Independent: true, Reference: "fixture-review"}}
	jsonFile := func(name string, value any) string {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return commandTestFile(t, name, string(data))
	}
	out = invoke([]string{"integration", "prepare"}, "--operation", "prepare-1", "--from", jsonFile("evidence.json", evidence))
	var prepared next.Receipt
	if err := json.Unmarshal([]byte(out), &prepared); err != nil {
		t.Fatal(err)
	}
	runGit("commit", "-m", "test: contribution")
	invoke([]string{"integration", "finish"}, "--operation", "finish-1", "--integration", prepared.IntegrationID)
	head := runGit("rev-parse", "HEAD")
	trial := next.UserTrial{Head: head, Response: "not-applicable", Reference: "document-1", NotApplicableReason: "Static fixture"}
	invoke([]string{"trial", "record"}, "--operation", "trial-1", "--from", jsonFile("trial.json", trial))
	v := next.Verification{Head: head, PlanRevision: 1, Reference: "fixture-report", Criteria: []next.Check{{Name: "AC1", Status: "pass", Reference: "fixture-log"}}, Tests: []next.Check{{Name: "static", Status: "pass", Reference: "fixture-log"}}, Review: evidence.Review}
	invoke([]string{"verification", "save"}, "--operation", "save-1", "--from", jsonFile("verification.json", v))
	invoke([]string{"verification", "complete"}, "--operation", "verify-1")
	runGit("remote", "add", "origin", "git@github.com:example/project.git")
	invoke([]string{"delivery", "start"}, "--operation", "delivery-1", "--base", "main")
	pr := next.PullRequest{Number: 1, URL: "https://github.com/example/project/pull/1", State: "OPEN", HeadRefName: "feature", HeadRefOID: head, BaseRefName: "main", StatusCheckRollup: []json.RawMessage{}}
	invoke([]string{"delivery", "observe"}, "--operation", "observe-1", "--from", jsonFile("pr-open.json", pr))
	invoke([]string{"delivery", "check"}, "--operation", "check-1")
	pr.State, pr.MergeCommit = "MERGED", &struct {
		OID string `json:"oid"`
	}{OID: head}
	invoke([]string{"delivery", "observe"}, "--operation", "observe-2", "--from", jsonFile("pr-merged.json", pr))
	invoke([]string{"delivery", "complete"}, "--operation", "delivered-1")
	invoke([]string{"suite", "publish"}, "--operation", "suite-1", "--reference", "fixture-suite")
	invoke([]string{"lease", "release"}, "--operation", "release-1")
	out, _, err = executeNext("suite", "show", "--campaign", receipt.CampaignID)
	if err != nil || !strings.Contains(out, "delivered") {
		t.Fatal(out, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".atelier")); !os.IsNotExist(err) {
		t.Fatal("legacy namespace was touched")
	}
}

func TestNextInvalidInputHasNoReceipt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, args := range [][]string{
		{"campaign", "start"},
		{"plan", "set", "--campaign", "bad"},
		{"integration", "prepare", "--token", "bad"},
		{"campaign", "status", "--campaign", "../../other"},
	} {
		out, _, err := executeNext(args...)
		if err == nil || out != "" {
			t.Fatal(args, out, err)
		}
	}
}
