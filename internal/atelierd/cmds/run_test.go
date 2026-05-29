package cmds

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/credentials"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/firebaseauth"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/firestore"
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

func TestClassifyShipError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want shipOutcome
	}{
		// A real token rejection from Firestore (401) or a revoked refresh
		// token must pause the daemon — this is the only path to auth-lost.
		{"firestore 401 -> auth-lost", &firestore.Error{Status: http.StatusUnauthorized}, shipOutcomeAuthLost},
		{"refresh token rejected -> auth-lost", &firebaseauth.AuthError{Status: http.StatusUnauthorized}, shipOutcomeAuthLost},
		// A 403 is a permission error on this specific write (e.g. a duplicate
		// /events doc) — it must be quarantined, never mistaken for auth loss.
		{"firestore 403 -> quarantine", &firestore.Error{Status: http.StatusForbidden}, shipOutcomeQuarantine},
		// Everything else is transient and retried with backoff.
		{"firestore 500 -> transient", &firestore.Error{Status: http.StatusInternalServerError}, shipOutcomeTransient},
		{"network error -> transient", errors.New("dial tcp: i/o timeout"), shipOutcomeTransient},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := classifyShipError(tc.err); got != tc.want {
				t.Fatalf("classifyShipError(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
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

func TestRunStateMarkUpdateCheck(t *testing.T) {
	t.Parallel()
	state := &runState{authState: status.AuthOk}

	if !state.lastUpdateCheckAt().IsZero() {
		t.Fatalf("new state should have zero last update check")
	}

	now := time.Now().UTC()
	state.markUpdateCheck(now)
	if !state.lastUpdateCheckAt().Equal(now) {
		t.Fatalf("markUpdateCheck did not persist, got %v want %v", state.lastUpdateCheckAt(), now)
	}
}

func TestCheckVersion(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		file     *status.File
		contains string
	}{
		{
			name:     "checked recently",
			file:     &status.File{Version: "0.7.0", LastUpdateCheckAt: time.Now().Add(-3 * time.Hour)},
			contains: "last update check",
		},
		{
			name:     "never checked",
			file:     &status.File{Version: "0.7.0"},
			contains: "no update check yet",
		},
		{
			name:     "dev build",
			file:     &status.File{Version: "dev"},
			contains: "dev build",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := checkVersion(tc.file)
			if got.tier != tierOK {
				t.Fatalf("checkVersion tier = %v, want OK (informational)", got.tier)
			}
			if !strings.Contains(got.note, tc.contains) {
				t.Fatalf("checkVersion note %q does not contain %q", got.note, tc.contains)
			}
		})
	}
}

type fakeUpgrader struct {
	upgradeErr   error
	installed    string
	installErr   error
	upgradeCalls int
}

func (f *fakeUpgrader) Upgrade(context.Context) error {
	f.upgradeCalls++
	return f.upgradeErr
}

func (f *fakeUpgrader) InstalledVersion(context.Context) (string, error) {
	return f.installed, f.installErr
}

func TestRunUpdateCheck(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		up          *fakeUpgrader
		wantRestart bool
	}{
		{"upgrade error: no restart", &fakeUpgrader{upgradeErr: errors.New("offline")}, false},
		{"installed version unreadable: no restart", &fakeUpgrader{installErr: errors.New("exec failed")}, false},
		{"installed empty: no restart", &fakeUpgrader{installed: ""}, false},
		{"installed unchanged: no restart", &fakeUpgrader{installed: Version}, false},
		{"installed changed: restart", &fakeUpgrader{installed: "9.9.9-test"}, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			state := &runState{authState: status.AuthOk}
			restarted := false
			runUpdateCheck(context.Background(), state, tc.up, func() { restarted = true })
			if restarted != tc.wantRestart {
				t.Fatalf("restart = %v, want %v", restarted, tc.wantRestart)
			}
			if tc.up.upgradeCalls != 1 {
				t.Fatalf("Upgrade called %d times, want exactly 1", tc.up.upgradeCalls)
			}
			if state.lastUpdateCheckAt().IsZero() {
				t.Fatal("runUpdateCheck must record the check time even on a no-op")
			}
		})
	}
}

func TestUpdaterLoopDevExemption(t *testing.T) {
	t.Parallel()
	// In tests Version is the dev sentinel (no -ldflags), so updaterLoop must
	// bail out before locating brew or requesting a restart. The guard keeps a
	// stamped build from spawning a real brew run here.
	if Version != devVersion {
		t.Skipf("Version is %q, not the dev sentinel", Version)
	}
	state := &runState{authState: status.AuthOk}
	restarted := false
	updaterLoop(context.Background(), state, func() { restarted = true })
	if restarted {
		t.Fatal("dev build must not request a restart")
	}
}
