package cmds

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/credentials"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/status"
)

func TestShouldRefreshNow(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	lead := 5 * time.Minute

	cases := []struct {
		name      string
		expiresAt time.Time
		want      bool
	}{
		{
			name:      "fresh token (>>lead): no refresh",
			expiresAt: now.Add(30 * time.Minute),
			want:      false,
		},
		{
			name:      "exactly at lead boundary: refresh",
			expiresAt: now.Add(lead),
			want:      true,
		},
		{
			name:      "inside lead window: refresh",
			expiresAt: now.Add(2 * time.Minute),
			want:      true,
		},
		{
			name:      "already expired: refresh",
			expiresAt: now.Add(-1 * time.Hour),
			want:      true,
		},
		{
			name:      "post-sleep wake-up — token expired hours ago: refresh",
			expiresAt: now.Add(-3 * time.Hour),
			want:      true,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := shouldRefreshNow(tc.expiresAt, lead, now)
			if got != tc.want {
				t.Fatalf("shouldRefreshNow(%v, %v, %v) = %v, want %v", tc.expiresAt, lead, now, got, tc.want)
			}
		})
	}
}

func TestShouldHandleCredentialsEvent(t *testing.T) {
	t.Parallel()
	credPath := "/home/test/.atelier/credentials"
	otherPath := "/home/test/.atelier/status"

	cases := []struct {
		name string
		ev   fsnotify.Event
		want bool
	}{
		{"create on credentials", fsnotify.Event{Name: credPath, Op: fsnotify.Create}, true},
		{"write on credentials", fsnotify.Event{Name: credPath, Op: fsnotify.Write}, true},
		{"rename on credentials", fsnotify.Event{Name: credPath, Op: fsnotify.Rename}, true},
		{"create + write composite", fsnotify.Event{Name: credPath, Op: fsnotify.Create | fsnotify.Write}, true},
		{"chmod-only on credentials", fsnotify.Event{Name: credPath, Op: fsnotify.Chmod}, false},
		{"remove-only on credentials", fsnotify.Event{Name: credPath, Op: fsnotify.Remove}, false},
		{"event on a different file in the same dir", fsnotify.Event{Name: otherPath, Op: fsnotify.Write}, false},
		{"empty op", fsnotify.Event{Name: credPath, Op: 0}, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := shouldHandleCredentialsEvent(tc.ev, credPath)
			if got != tc.want {
				t.Fatalf("shouldHandleCredentialsEvent(%+v, %q) = %v, want %v", tc.ev, credPath, got, tc.want)
			}
		})
	}
}

func TestShouldHandleCredentialsEvent_OnlyExactPath(t *testing.T) {
	t.Parallel()
	// Watching the root dir surfaces events on every file inside — we must
	// only react to the credentials file, never to status/log writes that
	// happen on every tick.
	root := "/home/test/.atelier"
	cred := filepath.Join(root, "credentials")

	for _, sibling := range []string{"status", "atelierd.log", "outbox", "credentials.tmp"} {
		ev := fsnotify.Event{Name: filepath.Join(root, sibling), Op: fsnotify.Write}
		if shouldHandleCredentialsEvent(ev, cred) {
			t.Fatalf("expected sibling %q to be ignored", sibling)
		}
	}
}

func TestRunStateAuthLostTransitions(t *testing.T) {
	t.Parallel()
	state := &runState{
		creds:     &credentials.Credentials{UID: "u", Email: "e"},
		host:      "h",
		authState: status.AuthOk,
		lastTick:  time.Now().UTC(),
	}

	if state.isAuthLost() {
		t.Fatalf("new state should be ok, got auth-lost")
	}

	// Mark + simulate a backoff in flight.
	state.markAuthLost("test")
	state.mu.Lock()
	state.currentBackoff = 30 * time.Second
	state.mu.Unlock()

	if !state.isAuthLost() {
		t.Fatalf("after markAuthLost: expected auth-lost")
	}

	// Clear must restore ok AND reset backoff so the loops don't inherit a
	// stale wait from the previous failure.
	state.clearAuthLost("re-link")
	if state.isAuthLost() {
		t.Fatalf("after clearAuthLost: expected ok")
	}
	state.mu.RLock()
	bo := state.currentBackoff
	state.mu.RUnlock()
	if bo != 0 {
		t.Fatalf("clearAuthLost should reset backoff to 0, got %s", bo)
	}

	// Re-clearing is idempotent.
	state.clearAuthLost("redundant")
	if state.isAuthLost() {
		t.Fatalf("idempotent clear should remain ok")
	}
}

func TestRunStateUpdateCreds(t *testing.T) {
	t.Parallel()
	state := &runState{
		creds:     &credentials.Credentials{UID: "old", IDToken: "old-token", IDTokenExpiresAt: time.Now()},
		authState: status.AuthOk,
	}

	// Snapshot returns a copy, so callers can read it after concurrent
	// updates without tearing.
	updated := &credentials.Credentials{UID: "new", IDToken: "new-token", IDTokenExpiresAt: time.Now().Add(time.Hour)}
	state.updateCreds(updated)

	got := state.currentCreds()
	if got.UID != "new" || got.IDToken != "new-token" {
		t.Fatalf("updateCreds did not propagate, got %+v", got)
	}

	// Mutating the returned copy must not affect the stored state.
	got.UID = "mutated"
	if state.currentCreds().UID != "new" {
		t.Fatalf("currentCreds should return a defensive copy")
	}
}

func TestDrainEvents_StopsAfterQuietWindow(t *testing.T) {
	t.Parallel()
	ch := make(chan fsnotify.Event, 4)
	ch <- fsnotify.Event{Name: "a", Op: fsnotify.Write}
	ch <- fsnotify.Event{Name: "b", Op: fsnotify.Write}
	// No more events — drainEvents should return after the quiet window.

	start := time.Now()
	drainEvents(ch, 50*time.Millisecond)
	elapsed := time.Since(start)

	if elapsed < 50*time.Millisecond {
		t.Fatalf("drainEvents returned too early: %s", elapsed)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("drainEvents took too long: %s", elapsed)
	}
}
