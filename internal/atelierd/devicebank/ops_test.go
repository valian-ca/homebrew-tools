package devicebank

import (
	"context"
	"io"
	"testing"
	"time"
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

// TestInitBankRejectsOversizedAndroidBank verifies that InitBank rejects
// nAndroid > MaxAndroidBank before any toolchain exec.
func TestInitBankRejectsOversizedAndroidBank(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	err := InitBank(context.Background(), 0, 17, io.Discard)
	if err == nil {
		t.Fatal("InitBank(0, 17) must error, got nil")
	}
	if err.Error() != "android bank size 17 exceeds the maximum of 16 (adb discovers console ports 5554-5584 only)" {
		t.Fatalf("InitBank(0, 17) error = %q, want message about maximum of 16", err.Error())
	}
}
