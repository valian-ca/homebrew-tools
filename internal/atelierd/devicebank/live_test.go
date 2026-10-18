//go:build devicebank_live

package devicebank

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/paths"
)

const (
	fitIOS      = "atelierfit-ios-1"
	fitAndroid  = "atelierfit-android-1"
	fitPort     = 5560
	leaseWait   = 6 * time.Minute
	offDeadline = 5 * time.Minute
)

type liveDriver struct {
	execDriver
	realHome string
}

// The emulator and its console run under the real HOME, as they do in
// production: only the bank state is isolated. Under a fresh HOME the
// emulator never finishes booting, and `adb emu kill` finds no console
// auth token, so the kill never happens.
func (d liveDriver) realHomeEnv() []string {
	var env []string
	for _, e := range androidEnv() {
		if !strings.HasPrefix(e, "HOME=") {
			env = append(env, e)
		}
	}
	return append(env, "HOME="+d.realHome)
}

func (d liveDriver) startAVD(avd string, port int, wipe bool) error {
	emu, err := emulatorPath()
	if err != nil {
		return err
	}
	return spawnDetached(d.realHomeEnv(), emu, emulatorArgs(avd, port, wipe)...)
}

func (d liveDriver) killAVD(ctx context.Context, serial string) error {
	kill := exec.CommandContext(ctx, "adb", "-s", serial, "emu", "kill")
	kill.Env = d.realHomeEnv()
	_ = kill.Run()
	deadline := time.Now().Add(cmdTimeout)
	for time.Now().Before(deadline) {
		serials, err := AdbSerials(ctx)
		if err == nil && !slices.Contains(serials, serial) {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("emulator %s still running after adb emu kill", serial)
}

func (liveDriver) physical(context.Context, Platform) ([]PhysicalDevice, error) { return nil, nil }

func (liveDriver) spawnRecycle(name string) error {
	go func() { _ = RunRecycle(context.Background(), name) }()
	return nil
}

func (liveDriver) notify(string, string) {}

func liveSetup(t *testing.T) (*Device, *Device) {
	t.Helper()
	ctx := context.Background()
	realHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	sdk := androidSDKRoot()
	t.Setenv("ANDROID_HOME", sdk)
	t.Setenv("ANDROID_USER_HOME", filepath.Join(realHome, ".android"))
	t.Setenv("ANDROID_AVD_HOME", filepath.Join(realHome, ".android", "avd"))
	t.Setenv("HOME", t.TempDir())
	prevDrv := drv
	drv = liveDriver{realHome: realHome}
	t.Cleanup(func() { drv = prevDrv })
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		if log, err := os.ReadFile(paths.Log()); err == nil {
			lines := strings.Split(string(log), "\n")
			t.Logf("atelierd.log tail:\n%s", strings.Join(lines[max(0, len(lines)-60):], "\n"))
		}
	})

	udid, err := CreateSimulator(ctx, fitIOS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = ShutdownSimulator(context.Background(), udid)
		_ = DeleteSimulator(context.Background(), udid)
	})
	if err := CreateAVD(ctx, fitAndroid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = liveDriver{realHome: realHome}.killAVD(context.Background(), "emulator-"+strconv.Itoa(fitPort))
		_ = DeleteAVD(context.Background(), fitAndroid)
	})
	forceRAM2G(t, filepath.Join(realHome, ".android", "avd", fitAndroid+".avd", "config.ini"))

	ios := &Device{Name: fitIOS, Platform: PlatformIOS, UDID: udid, State: StateOff, LastUsedAt: time.Now()}
	android := &Device{Name: fitAndroid, Platform: PlatformAndroid, AVD: fitAndroid, Port: fitPort, State: StateOff, LastUsedAt: time.Now()}
	seed(t, &State{Config: Config{IOS: 1, Android: 1}, Devices: []*Device{ios, android}})
	return ios, android
}

func forceRAM2G(t *testing.T, configPath string) {
	t.Helper()
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		if !strings.HasPrefix(sc.Text(), "hw.ramSize") {
			lines = append(lines, sc.Text())
		}
	}
	lines = append(lines, "hw.ramSize=2G")
	if err := os.WriteFile(configPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func liveLease(t *testing.T, session string, platform Platform) string {
	t.Helper()
	id, err := Acquire(context.Background(), LeaseRequest{Session: session, Workdir: "/wd", Platform: platform, Wait: leaseWait}, os.Stderr)
	if err != nil {
		t.Fatalf("lease %s %s: %v", session, platform, err)
	}
	return id
}

func run(t *testing.T, name string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func waitOff(t *testing.T, name string) {
	t.Helper()
	start := time.Now()
	for time.Since(start) < offDeadline {
		if err := Reap(context.Background()); err != nil {
			t.Fatal(err)
		}
		if load(t).FindDevice(name).State == StateOff {
			t.Logf("%s off %s after release", name, time.Since(start).Round(time.Second))
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("%s not off within %s, state %s", name, offDeadline, load(t).FindDevice(name).State)
}

func assertNothingUnleasedOn(t *testing.T, udid, serial string) {
	t.Helper()
	booted, err := run(t, "xcrun", "simctl", "list", "devices", "booted")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(booted, udid) {
		t.Fatalf("simctl lists the fitness simulator booted without a lease:\n%s", booted)
	}
	serials, err := AdbSerials(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(serials, serial) {
		t.Fatalf("adb lists the fitness emulator without a lease: %v", serials)
	}
}

var memTotal = regexp.MustCompile(`MemTotal:\s+(\d+) kB`)

// Boots a real simulator and a real AVD (~9 GB): run it only on a slot
// nobody else needs.
func TestLiveBank(t *testing.T) {
	ios, android := liveSetup(t)
	serial := android.TargetID()

	t.Run("L2 AVD provisioned at 2 GB boots with 4 GB", func(t *testing.T) {
		if got := liveLease(t, "live-a", PlatformAndroid); got != serial {
			t.Fatalf("lease returned %q, want %q", got, serial)
		}
		out, err := run(t, "adb", "-s", serial, "shell", "cat", "/proc/meminfo")
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		m := memTotal.FindStringSubmatch(out)
		if m == nil {
			t.Fatalf("no MemTotal in:\n%s", out)
		}
		kb, _ := strconv.Atoi(m[1])
		t.Logf("MemTotal %d kB", kb)
		if kb < 3_500*1024 {
			t.Fatalf("MemTotal %d kB, want about 4 GB", kb)
		}
	})

	t.Run("L3 android pause keeps data", func(t *testing.T) {
		if out, err := run(t, "adb", "-s", serial, "shell", "touch", "/data/local/tmp/atelierfit-marker"); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		if err := Pause(context.Background(), "live-a"); err != nil {
			t.Fatal(err)
		}
		serials, err := AdbSerials(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(serials, serial) {
			t.Fatal("a paused AVD must be off")
		}
		if l := load(t).FindLease("live-a", PlatformAndroid); l == nil || !l.Suspended() {
			t.Fatalf("the lease must be suspended, got %+v", l)
		}
		if got := liveLease(t, "live-a", PlatformAndroid); got != serial {
			t.Fatalf("resume returned %q", got)
		}
		if out, err := run(t, "adb", "-s", serial, "shell", "ls", "/data/local/tmp/atelierfit-marker"); err != nil {
			t.Fatalf("the marker must survive pause + resume: %v %s", err, out)
		}
	})

	t.Run("L1 android cold recycle", func(t *testing.T) {
		if err := Release(context.Background(), "live-a", ""); err != nil {
			t.Fatal(err)
		}
		waitOff(t, fitAndroid)
		assertNothingUnleasedOn(t, ios.UDID, serial)
	})

	t.Run("L3 android recycle erases", func(t *testing.T) {
		liveLease(t, "live-b", PlatformAndroid)
		if out, err := run(t, "adb", "-s", serial, "shell", "ls", "/data/local/tmp/atelierfit-marker"); err == nil {
			t.Fatalf("the marker must be gone after release + recycle: %s", out)
		}
		if err := Release(context.Background(), "live-b", ""); err != nil {
			t.Fatal(err)
		}
		waitOff(t, fitAndroid)
	})

	t.Run("L4 ios device stopped by hand", func(t *testing.T) {
		liveLease(t, "live-c", PlatformIOS)
		if out, err := run(t, "xcrun", "simctl", "shutdown", ios.UDID); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		if err := Reap(context.Background()); err != nil {
			t.Fatal(err)
		}
		s := load(t)
		if d := s.FindDevice(fitIOS); d.State != StateOff {
			t.Fatalf("a simulator stopped by hand must be off at the next reap, got %s", d.State)
		}
		if l := s.FindLease("live-c", PlatformIOS); l == nil || !l.DeviceDied {
			t.Fatalf("the lease must say device died, got %+v", l)
		}
		liveLease(t, "live-c", PlatformIOS)
		if booted, _ := run(t, "xcrun", "simctl", "list", "devices", "booted"); !strings.Contains(booted, ios.UDID) {
			t.Fatal("the next lease must restart the dead simulator")
		}
	})

	t.Run("L3 ios pause keeps data", func(t *testing.T) {
		if out, err := run(t, "xcrun", "simctl", "spawn", ios.UDID, "defaults", "write", "com.valian.atelierfit", "marker", "1"); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		if err := Pause(context.Background(), "live-c"); err != nil {
			t.Fatal(err)
		}
		if booted, _ := run(t, "xcrun", "simctl", "list", "devices", "booted"); strings.Contains(booted, ios.UDID) {
			t.Fatal("a paused simulator must be off")
		}
		liveLease(t, "live-c", PlatformIOS)
		if out, err := run(t, "xcrun", "simctl", "spawn", ios.UDID, "defaults", "read", "com.valian.atelierfit", "marker"); err != nil || out != "1" {
			t.Fatalf("the marker must survive pause + resume: %v %q", err, out)
		}
	})

	t.Run("L1 ios cold recycle erases", func(t *testing.T) {
		if err := Release(context.Background(), "live-c", ""); err != nil {
			t.Fatal(err)
		}
		waitOff(t, fitIOS)
		assertNothingUnleasedOn(t, ios.UDID, serial)
		liveLease(t, "live-d", PlatformIOS)
		if out, err := run(t, "xcrun", "simctl", "spawn", ios.UDID, "defaults", "read", "com.valian.atelierfit", "marker"); err == nil {
			t.Fatalf("the marker must be gone after release + recycle: %q", out)
		}
		if err := Release(context.Background(), "live-d", ""); err != nil {
			t.Fatal(err)
		}
		waitOff(t, fitIOS)
	})
}
