package cmds

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/outbox"
)

func desktopStoreDir(t *testing.T) string {
	t.Helper()
	home, _ := os.UserHomeDir()
	d := filepath.Join(home, "Library", "Application Support", "Claude", "claude-code-sessions", "acct", "ws")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	return d
}

func writeDesktopSession(t *testing.T, dir, file, cli, title, src string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"cliSessionId":   cli,
		"title":          title,
		"titleSource":    src,
		"lastActivityAt": time.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readOutbox(t *testing.T) []*outbox.Envelope {
	t.Helper()
	files, err := outbox.List()
	if err != nil {
		t.Fatal(err)
	}
	var out []*outbox.Envelope
	for _, f := range files {
		e, rerr := outbox.Read(f)
		if rerr != nil {
			t.Fatal(rerr)
		}
		out = append(out, e)
	}
	return out
}

func waitOutbox(t *testing.T, want int) []*outbox.Envelope {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if envs := readOutbox(t); len(envs) >= want {
			return envs
		}
		time.Sleep(20 * time.Millisecond)
	}
	return readOutbox(t)
}

// TestSessionStoreWatcherLoop_EndToEnd drives the real watcher goroutine
// (fsnotify + poll + reconcile + consume + derive + state + outbox) against a
// sandboxed HOME and a fake Desktop store, covering the title lifecycle:
// startup capture, dedup on unchanged rewrite, auto→user rename, and emptied
// title. It pins the integration the unit tests can't — the loop wiring itself.
func TestSessionStoreWatcherLoop_EndToEnd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	orig := sessionPollInterval
	sessionPollInterval = 40 * time.Millisecond
	defer func() { sessionPollInterval = orig }()

	store := desktopStoreDir(t)
	writeDesktopSession(t, store, "local_1.json", "cs-desktop-1", "Initial title", "auto")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { sessionStoreWatcherLoop(ctx, nil); close(done) }()

	envs := waitOutbox(t, 1)
	if len(envs) != 1 {
		t.Fatalf("startup capture: expected 1 envelope, got %d (%#v)", len(envs), envs)
	}
	if envs[0].Type != "transcript:ai-title" || envs[0].ClaudeSessionID != "cs-desktop-1" || envs[0].Payload["title"] != "Initial title" {
		t.Fatalf("startup capture wrong envelope: %#v", envs[0])
	}

	writeDesktopSession(t, store, "local_1.json", "cs-desktop-1", "Initial title", "auto")
	time.Sleep(250 * time.Millisecond)
	if got := readOutbox(t); len(got) != 1 {
		t.Fatalf("dedup: rewrite with identical title should emit nothing, got %d envelopes", len(got))
	}

	writeDesktopSession(t, store, "local_1.json", "cs-desktop-1", "Renamed by user", "user")
	envs = waitOutbox(t, 2)
	if len(envs) < 2 {
		t.Fatalf("rename: expected a 2nd envelope, got %d", len(envs))
	}
	last := envs[len(envs)-1]
	if last.Type != "transcript:custom-title" || last.Payload["title"] != "Renamed by user" {
		t.Fatalf("rename wrong envelope: %#v", last)
	}

	writeDesktopSession(t, store, "local_1.json", "cs-desktop-1", "", "user")
	envs = waitOutbox(t, 3)
	if len(envs) < 3 {
		t.Fatalf("emptied title: expected a 3rd envelope, got %d", len(envs))
	}
	last = envs[len(envs)-1]
	if last.Payload["title"] != "" {
		t.Fatalf("emptied title wrong payload: %#v", last)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not stop on ctx cancel")
	}
}
