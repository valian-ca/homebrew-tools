package devicebank

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/paths"
)

func seedLeased(t *testing.T, renewedAt time.Time) {
	t.Helper()
	s := bank(iosDevice(1), iosDevice(2))
	commitLease(s, &Candidate{Device: s.Devices[0]}, "sess-a", "/wd", PlatformIOS, t0)
	s.Leases[0].RenewedAt = renewedAt
	s.Devices[0].Booting = false
	s.transitions = nil
	seed(t, s)
}

// TestOnEmitRenewsAndNoOps covers OnEmit's branches: no bank file, empty
// session, renewal, and skipped writes.
func TestOnEmitRenewsAndNoOps(t *testing.T) {
	t.Run("no bank file", func(t *testing.T) {
		useFake(t)
		OnEmit("sess-a", false)
		if Exists() {
			t.Fatal("OnEmit must not create state file on fresh machine")
		}
	})

	t.Run("empty session", func(t *testing.T) {
		useFake(t)
		seedLeased(t, t0)
		OnEmit("", false)
		if l := load(t).FindLease("sess-a", PlatformIOS); !l.RenewedAt.Equal(t0) {
			t.Fatalf("empty session must not renew, RenewedAt=%v", l.RenewedAt)
		}
	})

	t.Run("renew on emit", func(t *testing.T) {
		useFake(t)
		old := time.Now().Add(-10 * time.Minute)
		seedLeased(t, old)
		OnEmit("sess-a", false)
		if l := load(t).FindLease("sess-a", PlatformIOS); !l.RenewedAt.After(old) {
			t.Fatalf("OnEmit must renew, RenewedAt=%v", l.RenewedAt)
		}
	})

	t.Run("non-holding session no-op", func(t *testing.T) {
		useFake(t)
		seedLeased(t, t0)
		OnEmit("other-session", false)
		if l := load(t).FindLease("sess-a", PlatformIOS); !l.RenewedAt.Equal(t0) {
			t.Fatalf("other session must not modify state, RenewedAt=%v", l.RenewedAt)
		}
	})
}

// fakeAndroidSDK builds an on-disk SDK skeleton so HasAndroidSDK() is true
// without any exec — sdkTool only stats the paths.
func fakeAndroidSDK(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("ANDROID_HOME", root)
	for _, p := range []string{"cmdline-tools/latest/bin/avdmanager", "emulator/emulator"} {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("fake"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// TestInitBankRejectsOversizedAndroidBank verifies the size cap fires before
// any toolchain exec — but only when the Android side would provision; a
// missing SDK keeps the degrade-with-warning contract.
func TestInitBankRejectsOversizedAndroidBank(t *testing.T) {
	useFake(t)
	fakeAndroidSDK(t)
	err := InitBank(context.Background(), 0, 17, io.Discard)
	if err == nil {
		t.Fatal("InitBank(0, 17) must error, got nil")
	}
	if err.Error() != "android bank size 17 exceeds the maximum of 16 (adb discovers console ports 5554-5584 only)" {
		t.Fatalf("InitBank(0, 17) error = %q, want message about maximum of 16", err.Error())
	}
}

func TestReleaseNoChangeAndPhysicalDrop(t *testing.T) {
	t.Run("non-holding session leaves the state file untouched", func(t *testing.T) {
		f := useFake(t)
		seedLeased(t, time.Now())
		f.setOn("UDID-1", true)
		before, err := os.ReadFile(paths.Devices())
		if err != nil {
			t.Fatal(err)
		}
		if err := Release(context.Background(), "sess-unknown", ""); err != nil {
			t.Fatal(err)
		}
		after, err := os.ReadFile(paths.Devices())
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Fatal("release of a non-holding session must not rewrite the state file")
		}
	})

	t.Run("platform-scoped release renews the other platform's lease", func(t *testing.T) {
		f := useFake(t)
		stale := time.Now().Add(-10 * time.Minute)
		seedLeased(t, stale)
		f.setOn("UDID-1", true)
		if err := WithLock(func(s *State) error {
			commitLease(s, &Candidate{Physical: &PhysicalDevice{ID: "serial-1", Name: "Pixel", Platform: PlatformAndroid}},
				"sess-a", "/wd", PlatformAndroid, time.Now())
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := Release(context.Background(), "sess-a", PlatformAndroid); err != nil {
			t.Fatal(err)
		}
		s := load(t)
		if s.FindLease("sess-a", PlatformAndroid) != nil {
			t.Fatal("android lease must be gone after platform-scoped release")
		}
		ios := s.FindLease("sess-a", PlatformIOS)
		if ios == nil || !ios.RenewedAt.After(stale) {
			t.Fatalf("the surviving lease must be renewed by the device command, got %+v", ios)
		}
	})

	t.Run("physical lease drop persists without a recycle", func(t *testing.T) {
		f := useFake(t)
		seed(t, &State{Config: Config{IOS: 1}})
		if err := WithLock(func(s *State) error {
			commitLease(s, &Candidate{Physical: &PhysicalDevice{ID: "serial-1", Name: "Pixel", Platform: PlatformAndroid}},
				"sess-a", "/wd", PlatformAndroid, time.Now())
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := Release(context.Background(), "sess-a", ""); err != nil {
			t.Fatal(err)
		}
		if load(t).FindLease("sess-a", PlatformAndroid) != nil {
			t.Fatal("physical lease must be gone after release")
		}
		if len(f.spawned) != 0 || f.count("") != 0 {
			t.Fatalf("a physical device is never recycled nor powered off, got %v %v", f.spawned, f.calls)
		}
	})
}
