package cmds

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/forge"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/outbox"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/paths"
)

func executeForge(args ...string) (string, string, error) {
	command := NewForgeCmd()
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs(args)
	command.SilenceErrors = true
	command.SilenceUsage = true
	err := command.Execute()
	return stdout.String(), stderr.String(), err
}

func commandTestFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func startCommandRun(t *testing.T, cap int) string {
	t.Helper()
	stdout, _, err := executeForge("run", "start", "VAL-306", "--session", "session-command", "--cap", fmt.Sprint(cap))
	if err != nil {
		t.Fatalf("run start: %v", err)
	}
	return strings.TrimSpace(stdout)
}

func TestForgeContractCommand(t *testing.T) {
	stdout, stderr, err := executeForge("contract")
	if err != nil {
		t.Fatalf("contract: %v", err)
	}
	if stdout != "1\n" || stderr != "" {
		t.Fatalf("contract stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestForgeCommandExitCodes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	unknown := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	_, _, err := executeForge("run", "status", "--run", unknown)
	if ExitCode(err) != ExitForgeUnknownRun {
		t.Fatalf("unknown run exit = %d, error %v", ExitCode(err), err)
	}
	runID := startCommandRun(t, 1)
	_, _, err = executeForge("pass", "next", "--run", runID, "--kind", "wave")
	if ExitCode(err) != ExitForgeInvalidPass {
		t.Fatalf("pass without open wave exit = %d, error %v", ExitCode(err), err)
	}
	_, _, err = executeForge("campaign", "load", "--run", runID)
	if ExitCode(err) != ExitForgeCampaign {
		t.Fatalf("campaign exit = %d, error %v", ExitCode(err), err)
	}
	if err := os.WriteFile(paths.ForgeCampaign(runID), []byte(`{"schemaVersion":2,"axes":[{"title":"A","scenarios":[{"title":"S","steps":["Do"],"expected":"Done"}]}]}`), 0o600); err != nil {
		t.Fatalf("write mismatched campaign: %v", err)
	}
	_, _, err = executeForge("campaign", "load", "--run", runID)
	if ExitCode(err) != ExitForgeCampaign {
		t.Fatalf("campaign schema exit = %d, error %v", ExitCode(err), err)
	}
	if err := os.Remove(paths.ForgeCampaign(runID)); err != nil {
		t.Fatalf("remove mismatched campaign: %v", err)
	}
	campaign := commandTestFile(t, "campaign.json", `{"schemaVersion":1,"axes":[{"title":"A","scenarios":[{"title":"S","steps":["Do"],"expected":"Done"}]}]}`)
	outcome := commandTestFile(t, "outcome.json", `{"schemaVersion":1,"outcomes":[{"axis":"A","scenario":"S","status":"finding"}]}`)
	if _, _, err := executeForge("campaign", "save", "--run", runID, "--from", campaign); err != nil {
		t.Fatalf("campaign save: %v", err)
	}
	if _, _, err := executeForge("wave", "open", "--run", runID); err != nil {
		t.Fatalf("wave open: %v", err)
	}
	_, _, err = executeForge("wave", "close", "--run", runID, "--findings", "0")
	if ExitCode(err) != ExitForgeInvalidPass {
		t.Fatalf("close without pass exit = %d, error %v", ExitCode(err), err)
	}
	if _, _, err := executeForge("pass", "next", "--run", runID, "--kind", "wave"); err != nil {
		t.Fatalf("pass next: %v", err)
	}
	if _, _, err := executeForge("outcome", "record", "--run", runID, "--pass", "wave-1", "--from", outcome); err != nil {
		t.Fatalf("outcome record: %v", err)
	}
	if _, _, err := executeForge("wave", "close", "--run", runID, "--findings", "1"); err != nil {
		t.Fatalf("wave close: %v", err)
	}
	_, _, err = executeForge("wave", "open", "--run", runID)
	if ExitCode(err) != ExitForgeWaveCap {
		t.Fatalf("wave cap exit = %d, error %v", ExitCode(err), err)
	}
	dryRun := startCommandRun(t, 2)
	dryCampaign := commandTestFile(t, "dry-campaign.json", `{"schemaVersion":1,"axes":[{"title":"A","scenarios":[{"title":"S","steps":["Do"],"expected":"Done"}]}]}`)
	dryOutcome := commandTestFile(t, "dry-outcome.json", `{"schemaVersion":1,"outcomes":[{"axis":"A","scenario":"S","status":"pass"}]}`)
	if _, _, err := executeForge("campaign", "save", "--run", dryRun, "--from", dryCampaign); err != nil {
		t.Fatalf("dry campaign save: %v", err)
	}
	if _, _, err := executeForge("wave", "open", "--run", dryRun); err != nil {
		t.Fatalf("dry wave open: %v", err)
	}
	if _, _, err := executeForge("pass", "next", "--run", dryRun, "--kind", "wave"); err != nil {
		t.Fatalf("dry pass next: %v", err)
	}
	if _, _, err := executeForge("outcome", "record", "--run", dryRun, "--pass", "wave-1", "--from", dryOutcome); err != nil {
		t.Fatalf("dry outcome record: %v", err)
	}
	if _, _, err := executeForge("wave", "close", "--run", dryRun, "--findings", "0"); err != nil {
		t.Fatalf("dry wave close: %v", err)
	}
	_, _, err = executeForge("wave", "open", "--run", dryRun)
	if ExitCode(err) != ExitForgeInvalidPass {
		t.Fatalf("dry terminal wave open exit = %d, error %v", ExitCode(err), err)
	}
	invalid := commandTestFile(t, "invalid.json", `{"schemaVersion":1,"axes":[]}`)
	_, _, err = executeForge("campaign", "save", "--run", runID, "--from", invalid)
	if ExitCode(err) != ExitForgeStaging {
		t.Fatalf("staging exit = %d, error %v", ExitCode(err), err)
	}
}

func TestForgePassCommandWithoutCampaign(t *testing.T) {
	validCampaign := `{"schemaVersion":1,"axes":[{"title":"A","scenarios":[{"title":"S","steps":["Do"],"expected":"Done"}]}]}`
	for _, kind := range []string{"wave", "review", "repair"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			runID := startCommandRun(t, 2)
			if kind == "wave" {
				if _, _, err := executeForge("wave", "open", "--run", runID); err != nil {
					t.Fatalf("wave open: %v", err)
				}
			}
			stdout, stderr, err := executeForge("pass", "next", "--run", runID, "--kind", kind)
			captureDir := strings.TrimSpace(stdout)
			if err != nil || stderr != "" || !filepath.IsAbs(captureDir) || filepath.Base(captureDir) != kind+"-1" {
				t.Fatalf("pass stdout=%q stderr=%q error=%v", stdout, stderr, err)
			}
			if info, err := os.Stat(captureDir); err != nil || !info.IsDir() {
				t.Fatalf("capture dir stat = %v, %v", info, err)
			}
			campaign := commandTestFile(t, "campaign.json", validCampaign)
			_, _, err = executeForge("campaign", "save", "--run", runID, "--from", campaign)
			if ExitCode(err) != ExitForgeStaging {
				t.Fatalf("campaign save after pass exit = %d, error %v", ExitCode(err), err)
			}
		})
	}
}

func TestForgeCommandsRejectInvalidCampaignWithoutMutation(t *testing.T) {
	t.Run("pass next", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		runID := startCommandRun(t, 2)
		if _, _, err := executeForge("wave", "open", "--run", runID); err != nil {
			t.Fatalf("wave open: %v", err)
		}
		if err := os.WriteFile(paths.ForgeCampaign(runID), []byte(`{"schemaVersion":1,"axes":[]}`), 0o600); err != nil {
			t.Fatalf("write invalid campaign: %v", err)
		}
		before, err := os.ReadFile(paths.ForgeRunState(runID))
		if err != nil {
			t.Fatalf("read state before: %v", err)
		}
		_, _, err = executeForge("pass", "next", "--run", runID, "--kind", "wave")
		if ExitCode(err) != ExitForgeCampaign {
			t.Fatalf("pass next exit = %d, error %v", ExitCode(err), err)
		}
		after, err := os.ReadFile(paths.ForgeRunState(runID))
		if err != nil {
			t.Fatalf("read state after: %v", err)
		}
		if !bytes.Equal(before, after) {
			t.Fatal("failed pass changed run state")
		}
		if _, err := os.Stat(paths.ForgeCaptures(runID)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed pass changed captures: %v", err)
		}
	})

	t.Run("wave close", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		runID := startCommandRun(t, 2)
		if _, _, err := executeForge("wave", "open", "--run", runID); err != nil {
			t.Fatalf("wave open: %v", err)
		}
		if _, _, err := executeForge("pass", "next", "--run", runID, "--kind", "wave"); err != nil {
			t.Fatalf("pass next: %v", err)
		}
		if err := os.WriteFile(paths.ForgeCampaign(runID), []byte(`{"schemaVersion":1,"axes":[]}`), 0o600); err != nil {
			t.Fatalf("write invalid campaign: %v", err)
		}
		before, err := os.ReadFile(paths.ForgeRunState(runID))
		if err != nil {
			t.Fatalf("read state before: %v", err)
		}
		_, _, err = executeForge("wave", "close", "--run", runID, "--findings", "0")
		if ExitCode(err) != ExitForgeCampaign {
			t.Fatalf("wave close exit = %d, error %v", ExitCode(err), err)
		}
		after, err := os.ReadFile(paths.ForgeRunState(runID))
		if err != nil {
			t.Fatalf("read state after: %v", err)
		}
		if !bytes.Equal(before, after) {
			t.Fatal("failed close changed run state")
		}
	})
}

func TestForgeCommandsWithSaturatedOutboxAndNoAuth(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(paths.Outbox(), 0o700); err != nil {
		t.Fatalf("mkdir outbox: %v", err)
	}
	const backlog = 1024
	for i := range backlog {
		name := filepath.Join(paths.Outbox(), fmt.Sprintf("backlog-%04d.json", i))
		if err := os.WriteFile(name, []byte("{}"), 0o600); err != nil {
			t.Fatalf("write backlog: %v", err)
		}
	}
	campaign := commandTestFile(t, "campaign.json", `{"schemaVersion":1,"axes":[{"title":"Navigation","scenarios":[{"title":"Open","steps":["Tap"],"expected":"Opened"}]}]}`)
	outcome := commandTestFile(t, "outcome.json", `{"schemaVersion":1,"outcomes":[{"axis":"Navigation","scenario":"Open","status":"pass"}]}`)
	if stdout, _, err := executeForge("contract"); err != nil || stdout != "1\n" {
		t.Fatalf("contract stdout=%q error=%v", stdout, err)
	}
	runID := startCommandRun(t, 2)
	stdout, _, err := executeForge("run", "status", "--run", runID)
	if err != nil || !strings.Contains(stdout, `"wave":0`) || strings.Count(stdout, "\n") != 1 {
		t.Fatalf("run status stdout=%q error=%v", stdout, err)
	}
	if _, _, err = executeForge("campaign", "save", "--run", runID, "--from", campaign); err != nil {
		t.Fatalf("campaign save: %v", err)
	}
	stdout, _, err = executeForge("campaign", "load", "--run", runID)
	if err != nil || !strings.Contains(stdout, `"schemaVersion": 1`) || strings.Count(stdout, "\n") < 2 {
		t.Fatalf("campaign load stdout=%q error=%v", stdout, err)
	}
	if stdout, _, err = executeForge("wave", "open", "--run", runID); err != nil || stdout != "1\n" {
		t.Fatalf("wave open stdout=%q error=%v", stdout, err)
	}
	stdout, _, err = executeForge("pass", "next", "--run", runID, "--kind", "wave")
	if err != nil || filepath.Base(strings.TrimSpace(stdout)) != "wave-1" || !filepath.IsAbs(strings.TrimSpace(stdout)) {
		t.Fatalf("pass next stdout=%q error=%v", stdout, err)
	}
	if _, _, err = executeForge("outcome", "record", "--run", runID, "--pass", "wave-1", "--from", outcome); err != nil {
		t.Fatalf("outcome record: %v", err)
	}
	if stdout, _, err = executeForge("wave", "close", "--run", runID, "--findings", "0"); err != nil || stdout != "dry\n" {
		t.Fatalf("wave close stdout=%q error=%v", stdout, err)
	}
	stdout, _, err = executeForge("summary", "--run", runID)
	if err != nil || !strings.Contains(stdout, "Latest normal wave: wave-1") {
		t.Fatalf("summary stdout=%q error=%v", stdout, err)
	}
	if _, _, err = executeForge("ref", "set", "comment-1", "--run", runID, "--key", "report"); err != nil {
		t.Fatalf("report ref set: %v", err)
	}
	if stdout, _, err = executeForge("ref", "get", "--run", runID, "--key", "report"); err != nil || stdout != "comment-1\n" {
		t.Fatalf("report ref get stdout=%q error=%v", stdout, err)
	}
	if _, _, err = executeForge("ref", "set", "document-1", "--run", runID, "--key", "testplan"); err != nil {
		t.Fatalf("testplan ref set: %v", err)
	}
	if stdout, _, err = executeForge("ref", "get", "--run", runID, "--key", "testplan"); err != nil || stdout != "document-1\n" {
		t.Fatalf("testplan ref get stdout=%q error=%v", stdout, err)
	}
	stdout, stderr, err := executeForge("testplan", "render", "--run", runID, "--lang", "de")
	if err != nil || !strings.HasPrefix(stdout, "# Test plan") || !strings.Contains(stderr, "falling back to en") {
		t.Fatalf("fallback render stdout=%q stderr=%q error=%v", stdout, stderr, err)
	}
	output := filepath.Join(t.TempDir(), "plan.md")
	if stdout, stderr, err = executeForge("testplan", "render", "--run", runID, "--lang", "fr", "--out", output); err != nil || stdout != output+"\n" || stderr != "" {
		t.Fatalf("file render stdout=%q stderr=%q error=%v", stdout, stderr, err)
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatalf("stat rendered file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("rendered mode = %o, want 600", info.Mode().Perm())
	}
	if _, err := os.Stat(paths.Credentials()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("credentials unexpectedly exist: %v", err)
	}
	count, err := outbox.Count()
	if err != nil {
		t.Fatalf("outbox.Count: %v", err)
	}
	if count != backlog+10 {
		t.Fatalf("outbox count = %d, want %d", count, backlog+10)
	}
}

func TestForgeRenderProtectedOutputUsesGenericExit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runID := startCommandRun(t, forge.DefaultCap)
	tests := []struct {
		name   string
		target func(t *testing.T) string
	}{
		{
			name: "direct protected file",
			target: func(_ *testing.T) string {
				return paths.ForgeRunState(runID)
			},
		},
		{
			name: "symlinked parent",
			target: func(t *testing.T) string {
				link := filepath.Join(t.TempDir(), "run-link")
				if err := os.Symlink(paths.ForgeRun(runID), link); err != nil {
					t.Fatalf("Symlink: %v", err)
				}
				return filepath.Join(link, "plan.md")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := executeForge("testplan", "render", "--run", runID, "--lang", "en", "--out", test.target(t))
			if err == nil || ExitCode(err) != 1 {
				t.Fatalf("exit = %d, error %v", ExitCode(err), err)
			}
		})
	}
}

func TestForgeCommandReturnsRunBusyWithinBound(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runID := startCommandRun(t, forge.DefaultCap)
	held := flock.New(paths.ForgeRunLock(runID))
	locked, err := held.TryLock()
	if err != nil || !locked {
		t.Fatalf("hold run lock: locked=%v err=%v", locked, err)
	}
	defer func() { _ = held.Unlock() }()

	started := time.Now()
	_, _, err = executeForge("run", "status", "--run", runID)
	elapsed := time.Since(started)
	if !errors.Is(err, forge.ErrRunBusy) {
		t.Fatalf("status error = %v, want ErrRunBusy", err)
	}
	if ExitCode(err) != 1 {
		t.Fatalf("busy exit = %d, want 1", ExitCode(err))
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("busy command took %s, want bounded wait", elapsed)
	}
}

func TestForgeHelpDocumentsStagingAndPositionalRef(t *testing.T) {
	stdout, _, err := executeForge("outcome", "record", "--help")
	if err != nil || !strings.Contains(stdout, `"status":"pass|finding|not_exercised"`) || !strings.Contains(stdout, "staging files never supply counts") {
		t.Fatalf("outcome help=%q error=%v", stdout, err)
	}
	stdout, _, err = executeForge("ref", "set", "--help")
	if err != nil || !strings.Contains(stdout, "set <value> --run <id> --key report|testplan") {
		t.Fatalf("ref set help=%q error=%v", stdout, err)
	}
}
