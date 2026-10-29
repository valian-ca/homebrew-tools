package generate

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/lanes/config"
	"github.com/valian-ca/homebrew-tools/internal/lanes/testutil"
)

func TestRunRendersOnceAndKeepsMtime(t *testing.T) {
	testutil.Hermetic(t)
	root := testutil.Repo(t)
	testutil.Write(t, filepath.Join(root, "app.env.tmpl"), "URL=http://127.0.0.1:{{.PORT_WEB}}\n")
	testutil.Write(t, filepath.Join(root, ".gitignore"), "app.env\n")
	specs := []config.Generate{{Template: "app.env.tmpl"}}
	out := filepath.Join(root, "app.env")

	var log bytes.Buffer
	if err := Run(context.Background(), root, specs, map[string]string{"PORT_WEB": "20000"}, &log); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(out)
	if string(data) != "URL=http://127.0.0.1:20000\n" {
		t.Fatalf("rendered %q", data)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(out, old, old); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), root, specs, map[string]string{"PORT_WEB": "20000"}, &log); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(out); !info.ModTime().Equal(old) {
		t.Fatalf("an unchanged render moved the mtime to %v", info.ModTime())
	}
	if log.Len() != 0 {
		t.Errorf("unexpected warning: %s", log.String())
	}
}

func TestRunRefusesTrackedOutputsAndWarnsOnUnignored(t *testing.T) {
	testutil.Hermetic(t)
	root := testutil.Repo(t)
	testutil.Write(t, filepath.Join(root, "a.tmpl"), "{{.X}}")
	testutil.Write(t, filepath.Join(root, "b.tmpl"), "{{.X}}")
	testutil.Commit(t, root, "a", "tracked\n")

	err := Run(context.Background(), root, []config.Generate{{Template: "a.tmpl"}}, map[string]string{"X": "1"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "tracked by git") {
		t.Fatalf("err = %v", err)
	}
	var log bytes.Buffer
	if err := Run(context.Background(), root, []config.Generate{{Template: "b.tmpl"}}, map[string]string{"X": "1"}, &log); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "not ignored") {
		t.Errorf("no warning for an unignored output: %q", log.String())
	}
}

func TestRunFailsOnUnknownVariable(t *testing.T) {
	testutil.Hermetic(t)
	root := testutil.Repo(t)
	testutil.Write(t, filepath.Join(root, "c.tmpl"), "{{.NOPE}}")
	if err := Run(context.Background(), root, []config.Generate{{Template: "c.tmpl"}}, map[string]string{}, &bytes.Buffer{}); err == nil {
		t.Fatal("rendered a template naming an unknown variable")
	}
}
