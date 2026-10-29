package firebase

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/valian-ca/homebrew-tools/internal/lanes/config"
	"github.com/valian-ca/homebrew-tools/internal/lanes/testutil"
)

func TestGenerateRefusesTrackedConfigAndLeavesItAlone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	testutil.Hermetic(t)
	root := testutil.RealTemp(t)
	testutil.Git(t, root, "init", "-q")
	testutil.Write(t, filepath.Join(root, "firebase.emulator.json"), `{"emulators":{"firestore":{}}}`)
	testutil.Write(t, filepath.Join(root, "firebase.lanes.json"), `{"tracked":true}`)
	testutil.Git(t, root, "add", "-A")
	testutil.Git(t, root, "commit", "-q", "-m", "x")

	cfg, err := config.Parse([]byte(`
schema: 1
ports: {block: 4, names: [firestore, firestore_ws, hub, test_hub]}
resources: [{firebase: {config: firebase.emulator.json}}]
`))
	if err != nil {
		t.Fatal(err)
	}

	err = Generate(context.Background(), root, cfg, 40000, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "is tracked by git") {
		t.Fatalf("err = %v, want a tracked-by-git refusal", err)
	}
	data, readErr := os.ReadFile(filepath.Join(root, "firebase.lanes.json"))
	if readErr != nil {
		t.Fatalf("tracked config was removed: %v", readErr)
	}
	if string(data) != `{"tracked":true}` {
		t.Fatalf("tracked config was rewritten: %q", data)
	}
}
