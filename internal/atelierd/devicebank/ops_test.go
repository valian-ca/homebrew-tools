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

// TestOnEmitRenewsAndNoOps covers OnEmit's exec-free branches:
// no bank file, empty session, renewal, and skipped writes.
func TestOnEmitRenewsAndNoOps(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(t *testing.T)
		session   string
		checkPost func(t *testing.T)
	}{
		{
			name: "no bank file",
			setup: func(t *testing.T) {
				t.Setenv("HOME", t.TempDir())
			},
			session: "sess-a",
			checkPost: func(t *testing.T) {
				if Exists() {
					t.Fatalf("OnEmit must not create state file on fresh machine")
				}
			},
		},
		{
			name: "empty session",
			setup: func(t *testing.T) {
				t.Setenv("HOME", t.TempDir())
				err := WithLock(func(s *State) error {
					*s = *bankOfTwo()
					commitLease(s, &Candidate{Device: s.Devices[0]}, "sess-a", "/wd", PlatformIOS, t0)
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			},
			session: "",
			checkPost: func(t *testing.T) {
				s, err := Load()
				if err != nil {
					t.Fatal(err)
				}
				l := s.FindLease("sess-a", PlatformIOS)
				if l == nil {
					t.Fatalf("lease must exist")
				}
				if !l.RenewedAt.Equal(t0) {
					t.Fatalf("empty session must not renew, RenewedAt=%v want %v", l.RenewedAt, t0)
				}
			},
		},
		{
			name: "renew on emit",
			setup: func(t *testing.T) {
				t.Setenv("HOME", t.TempDir())
				err := WithLock(func(s *State) error {
					*s = *bankOfTwo()
					c := &Candidate{Device: s.Devices[0]}
					l := commitLease(s, c, "sess-a", "/wd", PlatformIOS, t0)
					l.RenewedAt = t0.Add(-10 * time.Minute)
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			},
			session: "sess-a",
			checkPost: func(t *testing.T) {
				s, err := Load()
				if err != nil {
					t.Fatal(err)
				}
				l := s.FindLease("sess-a", PlatformIOS)
				if l == nil {
					t.Fatalf("lease must exist")
				}
				oldTime := t0.Add(-10 * time.Minute)
				if !l.RenewedAt.After(oldTime) {
					t.Fatalf("OnEmit must renew, RenewedAt=%v was not after %v", l.RenewedAt, oldTime)
				}
			},
		},
		{
			name: "non-holding session no-op",
			setup: func(t *testing.T) {
				t.Setenv("HOME", t.TempDir())
				err := WithLock(func(s *State) error {
					*s = *bankOfTwo()
					commitLease(s, &Candidate{Device: s.Devices[0]}, "sess-a", "/wd", PlatformIOS, t0)
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			},
			session: "other-session",
			checkPost: func(t *testing.T) {
				s, err := Load()
				if err != nil {
					t.Fatal(err)
				}
				l := s.FindLease("sess-a", PlatformIOS)
				if l == nil {
					t.Fatalf("lease must exist")
				}
				if !l.RenewedAt.Equal(t0) {
					t.Fatalf("other session must not modify state, RenewedAt=%v want %v", l.RenewedAt, t0)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup(t)
			OnEmit(tt.session, false)
			tt.checkPost(t)
		})
	}
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
	t.Setenv("HOME", t.TempDir())
	fakeAndroidSDK(t)
	err := InitBank(context.Background(), 0, 17, io.Discard)
	if err == nil {
		t.Fatal("InitBank(0, 17) must error, got nil")
	}
	if err.Error() != "android bank size 17 exceeds the maximum of 16 (adb discovers console ports 5554-5584 only)" {
		t.Fatalf("InitBank(0, 17) error = %q, want message about maximum of 16", err.Error())
	}
}

// TestReleaseNoChangeAndPhysicalDrop covers Release's exec-free branches:
// a non-holding session skips the state write, a physical-lease drop
// persists without spawning any recycle worker.
func TestReleaseNoChangeAndPhysicalDrop(t *testing.T) {
	t.Run("non-holding session leaves the state file untouched", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		err := WithLock(func(s *State) error {
			*s = *bankOfTwo()
			now := time.Now()
			// Fresh timestamps keep reapLocked's idle pass empty — an idle
			// device would trigger a real simctl exec inside the unit test.
			for _, d := range s.Devices {
				d.LastUsedAt = now
			}
			commitLease(s, &Candidate{Device: s.Devices[0]}, "sess-a", "/wd", PlatformIOS, now)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
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

	t.Run("physical lease drop persists", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		err := WithLock(func(s *State) error {
			*s = State{Config: Config{IOS: 1}}
			commitLease(s, &Candidate{Physical: &PhysicalDevice{ID: "serial-1", Name: "Pixel", Platform: PlatformAndroid}},
				"sess-a", "/wd", PlatformAndroid, time.Now())
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := Release(context.Background(), "sess-a", ""); err != nil {
			t.Fatal(err)
		}
		s, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if s.FindLease("sess-a", PlatformAndroid) != nil {
			t.Fatal("physical lease must be gone after release")
		}
	})
}
