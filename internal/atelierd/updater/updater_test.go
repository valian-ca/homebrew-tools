package updater

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeFakeExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestBrewPathFrom(t *testing.T) {
	t.Parallel()

	t.Run("prefix wins when its brew is executable", func(t *testing.T) {
		t.Parallel()
		prefix := t.TempDir()
		writeFakeExecutable(t, filepath.Join(prefix, "bin", "brew"))
		got, err := brewPathFrom(prefix, []string{"/nonexistent/brew"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if want := filepath.Join(prefix, "bin", "brew"); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("falls back to probes when prefix unset", func(t *testing.T) {
		t.Parallel()
		probe := filepath.Join(t.TempDir(), "brew")
		writeFakeExecutable(t, probe)
		got, err := brewPathFrom("", []string{"/nonexistent/brew", probe})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != probe {
			t.Fatalf("got %q, want %q", got, probe)
		}
	})

	t.Run("errors when nothing is executable", func(t *testing.T) {
		t.Parallel()
		if _, err := brewPathFrom("", []string{"/nonexistent/brew"}); err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("non-executable file is rejected", func(t *testing.T) {
		t.Parallel()
		plain := filepath.Join(t.TempDir(), "brew")
		if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := brewPathFrom("", []string{plain}); err == nil {
			t.Fatal("expected error for non-executable, got nil")
		}
	})
}

func TestUpgradeRunsUpdateThenTargetedUpgrade(t *testing.T) {
	t.Parallel()
	var calls [][]string
	u := &Updater{
		brewPath: "/fake/bin/brew",
		run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			calls = append(calls, append([]string{name}, args...))
			return nil, nil
		},
	}
	if err := u.Upgrade(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := [][]string{
		{"/fake/bin/brew", "update"},
		{"/fake/bin/brew", "upgrade", Formula},
	}
	if len(calls) != len(want) {
		t.Fatalf("got %d calls, want %d: %v", len(calls), len(want), calls)
	}
	for i := range want {
		if len(calls[i]) != len(want[i]) {
			t.Fatalf("call %d: got %v, want %v", i, calls[i], want[i])
		}
		for j := range want[i] {
			if calls[i][j] != want[i][j] {
				t.Fatalf("call %d: got %v, want %v", i, calls[i], want[i])
			}
		}
	}
}

func TestUpgradeStopsOnUpdateFailure(t *testing.T) {
	t.Parallel()
	var calls int
	u := &Updater{
		brewPath: "/fake/bin/brew",
		run: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			calls++
			return []byte("no network"), errors.New("exit 1")
		},
	}
	if err := u.Upgrade(context.Background()); err == nil {
		t.Fatal("expected error, got nil")
	}
	if calls != 1 {
		t.Fatalf("brew upgrade should not run after brew update fails; got %d calls", calls)
	}
}

func TestInstalledVersionParsesBinaryOutput(t *testing.T) {
	t.Parallel()
	var gotBin string
	var gotArgs []string
	u := &Updater{
		brewPath: "/fake/bin/brew",
		run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			gotBin = name
			gotArgs = args
			return []byte("atelierd version 1.2.3\n"), nil
		},
	}
	v, err := u.InstalledVersion(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "1.2.3" {
		t.Fatalf("got %q, want %q", v, "1.2.3")
	}
	if gotBin != "/fake/bin/atelierd" {
		t.Fatalf("queried %q, want /fake/bin/atelierd", gotBin)
	}
	if len(gotArgs) != 1 || gotArgs[0] != "--version" {
		t.Fatalf("queried args %v, want [--version]", gotArgs)
	}
}

func TestParseVersion(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{"atelierd version 1.2.3\n", "1.2.3"},
		{"atelierd version dev", "dev"},
		{"1.2.3", "1.2.3"},
		{"", ""},
		{"   ", ""},
	}
	for _, tc := range cases {
		if got := parseVersion(tc.in); got != tc.want {
			t.Fatalf("parseVersion(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
