package cmds

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckRejected_PrintsTheCleanupCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".atelier", "outbox")
	writeJSONL(t, filepath.Join(dir, "cs-1_202610101003.json.rejected"), `{}`)
	writeJSONL(t, filepath.Join(dir, "01HZZZZZZZZZZZZZZZZZZZZZZZ.json.rejected"), `{}`)

	got := checkRejected()

	if got.tier != tierWarn {
		t.Errorf("tier = %v, want warn", got.tier)
	}
	want := "rm " + dir + "/*.json.rejected"
	if !strings.Contains(got.note, want) {
		t.Errorf("note = %q, want it to contain %q", got.note, want)
	}
	if !strings.HasPrefix(got.note, "2 event(s) quarantined") {
		t.Errorf("note = %q, want the count first", got.note)
	}
}

func TestCheckRejected_NoneWhenEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got := checkRejected(); got.tier != tierOK || got.note != "none" {
		t.Errorf("checkRejected = %+v, want ok / none", got)
	}
}

func TestRejectedCleanupCommand_QuotesUnsafePaths(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"/Users/fp/.atelier/outbox":       "rm /Users/fp/.atelier/outbox/*.json.rejected",
		"/Users/Jane Doe/.atelier/outbox": "rm '/Users/Jane Doe/.atelier/outbox'/*.json.rejected",
		"/Users/o'neil/.atelier/outbox":   `rm '/Users/o'\''neil/.atelier/outbox'/*.json.rejected`,
	}
	for dir, want := range cases {
		if got := rejectedCleanupCommand(dir); got != want {
			t.Errorf("rejectedCleanupCommand(%q) = %q, want %q", dir, got, want)
		}
	}
}

func TestRejectedCleanupCommand_RemovesOnlyQuarantinedFiles(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	dir := filepath.Join(t.TempDir(), "out box")
	writeJSONL(t, filepath.Join(dir, "a.json.rejected"), `{}`)
	writeJSONL(t, filepath.Join(dir, "b.json"), `{}`)

	if out, err := runShell(rejectedCleanupCommand(dir)); err != nil {
		t.Fatalf("cleanup command failed: %v: %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.json.rejected")); !os.IsNotExist(err) {
		t.Error("quarantined file survived the cleanup command")
	}
	if _, err := os.Stat(filepath.Join(dir, "b.json")); err != nil {
		t.Errorf("pending event removed by the cleanup command: %v", err)
	}
}

func runShell(cmd string) ([]byte, error) {
	return exec.Command("/bin/sh", "-c", cmd).CombinedOutput()
}
