package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/valian-ca/homebrew-tools/internal/lanes/cli"
)

func run(t *testing.T, args ...string) string {
	t.Helper()
	root := cli.NewRoot("test", contract)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatalf("lanes %s: %v\n%s", strings.Join(args, " "), err, out.String())
	}
	return out.String()
}

func TestHelpContractPrintsTheReadme(t *testing.T) {
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	if got := run(t, "help", "contract"); got != string(readme) {
		t.Fatalf("lanes help contract differs from README.md (%d bytes, want %d)", len(got), len(readme))
	}
	if got := run(t, "help", "start"); !strings.Contains(got, "lanes start") {
		t.Errorf("lanes help start no longer prints start's help:\n%s", got)
	}
}

func TestHelpPointsToTheEmbeddedContract(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"help"}, {"hook", "--help"}} {
		out := run(t, args...)
		if !strings.Contains(out, "lanes help contract") {
			t.Errorf("lanes %s does not point to lanes help contract:\n%s", strings.Join(args, " "), out)
		}
		if strings.Contains(out, "github.com/valian-ca/homebrew-tools/blob") {
			t.Errorf("lanes %s cites the contract's GitHub URL", strings.Join(args, " "))
		}
	}
}

func TestStopHelpSparesTheMySQLServer(t *testing.T) {
	if out := run(t, "stop", "--help"); !strings.Contains(out, "server is shared by the project and keeps running") {
		t.Errorf("lanes stop --help says nothing of the shared MySQL server:\n%s", out)
	}
}

func TestDemoSeedHelpAppendsAtEveryRun(t *testing.T) {
	out, err := exec.Command(demo+"tool/mysql-seed", "--help").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "At every run, it appends") || strings.Contains(string(out), "Before seeding") {
		t.Errorf("mysql-seed --help:\n%s", out)
	}
}

func TestContractDocumentsContextAndState(t *testing.T) {
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"### Context block",
		"`lanes help contract`",
		"`never started`",
		"**Limit:** a `cd`",
		"Run `lanes help contract` for the contract",
		"`~/.local/state/lanes/mysql-<port>.lock`",
		"deferred erase it retries for a live worktree — connects with the main\ncheckout's",
	} {
		if !strings.Contains(string(readme), want) {
			t.Errorf("the contract lacks %q", want)
		}
	}
	if strings.Contains(string(readme), "](../") || strings.Contains(string(readme), "](./") {
		t.Error("the contract links a path a reader of the binary cannot open")
	}
}
