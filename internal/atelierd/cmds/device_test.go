package cmds

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/devicebank"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/forge"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/paths"
)

func TestStatusShowsOnSinceAndQueue(t *testing.T) {
	now := time.Date(2026, 10, 17, 15, 30, 0, 0, time.Local)
	on := &devicebank.Device{Name: "atelier-android-1", Platform: devicebank.PlatformAndroid, State: devicebank.StateOn, PoweredOnAt: now.Add(-26 * time.Minute)}
	broken := &devicebank.Device{Name: "atelier-android-2", Platform: devicebank.PlatformAndroid, State: devicebank.StateBroken, BrokenReason: "emulator emulator-5582 still running after adb emu kill"}
	suspended := &devicebank.Device{Name: "atelier-ios-1", Platform: devicebank.PlatformIOS, State: devicebank.StateOff}
	rows := []devicebank.StatusRow{
		{Name: on.Name, Platform: on.Platform, State: on.State, Device: on,
			Lease: &devicebank.Lease{SessionID: "S1", AcquiredAt: on.PoweredOnAt, RenewedAt: now.Add(-12 * time.Second)}},
		{Name: broken.Name, Platform: broken.Platform, State: broken.State, Device: broken},
		{Name: suspended.Name, Platform: suspended.Platform, State: suspended.State, Device: suspended,
			Lease: &devicebank.Lease{SessionID: "S3", SuspendedAt: now.Add(-10 * time.Minute)}},
		{Name: "atelier-ios-2", Platform: devicebank.PlatformIOS, State: devicebank.StateOff,
			Device: &devicebank.Device{Name: "atelier-ios-2", Platform: devicebank.PlatformIOS, State: devicebank.StateOff},
			Lease:  &devicebank.Lease{SessionID: "S4", DeviceDied: true}},
	}
	queue := []*devicebank.Waiter{{SessionID: "S2", Platform: devicebank.PlatformAndroid, EnqueuedAt: now.Add(-18 * time.Minute)}}

	var out bytes.Buffer
	if err := renderStatus(&out, rows, queue, now); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"session S1, on since 15:04, renewed 12s ago",
		"broken: emulator emulator-5582 still running after adb emu kill",
		"session S3, suspended since 15:20",
		"session S4, device died — restarts at next lease",
		"QUEUE\n1. session S2 (android) waiting since 15:12",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestStatusPhysicalRowNameAndID(t *testing.T) {
	now := time.Date(2026, 10, 17, 15, 30, 0, 0, time.Local)
	rows := []devicebank.StatusRow{
		{Name: "atelier-android-1", Platform: devicebank.PlatformAndroid, State: devicebank.StateOff, ID: "emulator-5560",
			Device: &devicebank.Device{Name: "atelier-android-1", State: devicebank.StateOff}},
		{Name: "Pixel 7 Pro", Platform: devicebank.PlatformAndroid, Physical: true, State: devicebank.StateOn, ID: "P7SERIAL", Serial: "P7SERIAL", Wired: true,
			Lease: &devicebank.Lease{SessionID: "S1", AcquiredAt: now.Add(-time.Minute), RenewedAt: now.Add(-5 * time.Second)}},
		{Name: "iPhone 12 Mini", Platform: devicebank.PlatformIOS, Physical: true, State: devicebank.StateOff, ID: "00008101-0011649C3EB8001E", Serial: "00008101-0011649C3EB8001E", Wired: true,
			Lease: &devicebank.Lease{SessionID: "S2", SuspendedAt: now.Add(-10 * time.Minute)}},
		{Name: "Galaxy", Platform: devicebank.PlatformAndroid, Physical: true, State: devicebank.StateOff, ID: "192.168.1.30:5555", Serial: "R5CT1234"},
		{Name: "Pixel 6", Platform: devicebank.PlatformAndroid, Physical: true, State: devicebank.StateOff, ID: "P6SERIAL", Serial: "P6SERIAL", Absent: true,
			Restore: &devicebank.PendingRestore{EndedAt: now.Add(-26 * time.Minute), LastError: "Pixel 6 is not connected"}},
		{Name: "Pixel 8", Platform: devicebank.PlatformAndroid, Physical: true, State: devicebank.StateOn, ID: "P8SERIAL", Serial: "P8SERIAL", Absent: true,
			Lease: &devicebank.Lease{SessionID: "S3", AcquiredAt: now.Add(-2 * time.Minute), RenewedAt: now.Add(-time.Minute)}},
		{Name: "Pixel 9", Platform: devicebank.PlatformAndroid, Physical: true, State: devicebank.StateOn, ID: "P9SERIAL", Serial: "P9SERIAL", Wired: true,
			Lease:   &devicebank.Lease{SessionID: "S4", AcquiredAt: now.Add(-time.Minute), RenewedAt: now},
			Restore: &devicebank.PendingRestore{EndedAt: now.Add(-30 * time.Minute)}},
	}
	var out bytes.Buffer
	if err := renderStatus(&out, rows, nil, now); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out.String(), "\n")
	if fields := strings.Fields(lines[0]); strings.Join(fields, " ") != "NAME PLATFORM TYPE STATE ID LEASE" {
		t.Fatalf("header = %q", lines[0])
	}
	pixelRows := 0
	for _, line := range lines {
		if strings.HasPrefix(line, "Pixel 7 Pro") {
			pixelRows++
			if got := strings.Join(strings.Fields(line)[:8], " "); got != "Pixel 7 Pro android physical on P7SERIAL session" {
				t.Errorf("Pixel row = %q", line)
			}
		}
	}
	if pixelRows != 1 {
		t.Errorf("the Pixel is listed once, got %d rows", pixelRows)
	}
	for _, want := range []string{
		"emulator-5560",
		"session S1, age 1m0s, renewed 5s ago",
		"session S2, suspended since 15:20",
		"192.168.1.30:5555 (serial R5CT1234)",
		"not leasable: wireless only",
		"restore pending since 15:04 (Pixel 6 is not connected)",
		"session S3, age 2m0s, renewed 1m0s ago · not connected",
		"session S4, age 1m0s, renewed 0s ago · restore pending since 15:00",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "(usb)") {
		t.Errorf("a physical name carries no transport:\n%s", out.String())
	}
}

func executeDevice(args ...string) (string, string, error) {
	command := NewDeviceCmd()
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs(args)
	command.SilenceErrors = true
	command.SilenceUsage = true
	err := command.Execute()
	return stdout.String(), stderr.String(), err
}

func TestLeaseTypeFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := devicebank.WithLock(func(s *devicebank.State) error {
		s.Config = devicebank.Config{Android: 1}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, _, err := executeDevice("lease", "--platform", "android", "--session", "S1", "--type", "both"); err == nil || !strings.Contains(err.Error(), `invalid type "both"`) {
		t.Fatalf("an unknown type is refused, got %v", err)
	}
	stdout, stderr, err := executeDevice("lease", "--platform", "android", "--session", "S1", "--type", "physical")
	if ExitCode(err) != ExitBankExhausted || stdout != "" {
		t.Fatalf("physical required and none leasable exits 10 with nothing on stdout, got %v %q", err, stdout)
	}
	if !strings.Contains(stderr, "no --app-id given") {
		t.Fatalf("stderr says why, got %q", stderr)
	}
}

func seedHolderAndWaiter(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	now := time.Now()
	state := devicebank.State{
		Config: devicebank.Config{IOS: 1},
		Devices: []*devicebank.Device{{Name: "atelier-ios-1", Platform: devicebank.PlatformIOS, UDID: "UDID-1",
			State: devicebank.StateOn, PoweredOnAt: now}},
		Leases: []*devicebank.Lease{{SessionID: "session-holder", Platform: devicebank.PlatformIOS,
			DeviceName: "atelier-ios-1", DeviceID: "UDID-1", AcquiredAt: now, RenewedAt: now}},
		Queue: []*devicebank.Waiter{{SessionID: "session-waiter", Platform: devicebank.PlatformIOS,
			EnqueuedAt: now, ExpiresAt: now.Add(time.Hour)}},
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.EnsureDir(paths.MustRoot()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Devices(), data, paths.FileMode); err != nil {
		t.Fatal(err)
	}
}

func TestHolderWarningOnForgeCommands(t *testing.T) {
	seedHolderAndWaiter(t)

	stdout, stderr, err := executeForge("run", "start", "VAL-349", "--session", "session-holder")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "warning: session session-waiter has been waiting") {
		t.Fatalf("run start of the holder must warn, stderr=%q", stderr)
	}
	runID := strings.TrimSpace(stdout)
	_, stderr, err = executeForge("run", "status", "--run", runID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "you hold atelier-ios-1") {
		t.Fatalf("a command of the holder's run must warn, stderr=%q", stderr)
	}

	stdout, _, err = executeForge("run", "start", "VAL-350", "--session", "session-other")
	if err != nil {
		t.Fatal(err)
	}
	_, stderr, err = executeForge("run", "status", "--run", strings.TrimSpace(stdout))
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("a run of another session is not warned, stderr=%q", stderr)
	}
}

func TestHolderWarningOnDeviceCommands(t *testing.T) {
	seedHolderAndWaiter(t)
	hook := NewDeviceCmd().PersistentPostRun
	for _, tt := range []struct {
		session string
		warned  bool
	}{{"session-holder", true}, {"session-waiter", false}, {"", false}} {
		sub := &cobra.Command{Use: "lease"}
		sub.Flags().String("session", tt.session, "")
		var stderr bytes.Buffer
		sub.SetErr(&stderr)
		hook(sub, nil)
		if got := stderr.Len() > 0; got != tt.warned {
			t.Errorf("session %q warned=%v, want %v (%q)", tt.session, got, tt.warned, stderr.String())
		}
	}
}

func TestExitCode(t *testing.T) {
	tests := []struct {
		err  error
		want int
	}{
		{nil, 0},
		{devicebank.ErrExhausted, ExitBankExhausted},
		{devicebank.ErrNotInitialized, ExitBankNotInitialized},
		{fmt.Errorf("acquire: %w", devicebank.ErrExhausted), ExitBankExhausted},
		{forge.ErrUnknownRun, ExitForgeUnknownRun},
		{forge.ErrInvalidPass, ExitForgeInvalidPass},
		{forge.ErrCampaignInvalid, ExitForgeCampaign},
		{forge.ErrWaveCap, ExitForgeWaveCap},
		{forge.ErrInvalidStaging, ExitForgeStaging},
		{forge.ErrAmbiguousRun, ExitForgeAmbiguousRun},
		{fmt.Errorf("wrapped: %w", forge.ErrInvalidStaging), ExitForgeStaging},
		{errors.New("anything else"), 1},
	}
	for _, tt := range tests {
		if got := ExitCode(tt.err); got != tt.want {
			t.Errorf("ExitCode(%v) = %d, want %d", tt.err, got, tt.want)
		}
	}
}
