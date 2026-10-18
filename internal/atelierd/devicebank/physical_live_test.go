//go:build devicebank_physical

package devicebank

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

type physicalLiveDriver struct{ execDriver }

func (physicalLiveDriver) spawnRecycle(name string) error {
	return errors.New("no virtual device may be recycled in the physical live run: " + name)
}

func (physicalLiveDriver) spawnRestore(deviceID string) error {
	return RunRestore(context.Background(), deviceID)
}

func (physicalLiveDriver) notify(string, string) {}

// The device tools run under the real HOME, as in production: under a fresh
// one, adb would start a server without this machine's keys. Only the bank
// state is isolated.
func physicalLiveSetup(t *testing.T) (pixel, iphone PhysicalDevice) {
	t.Helper()
	realHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "HOME=") {
			env = append(env, e)
		}
	}
	physicalToolEnv = append(env, "HOME="+realHome)
	t.Setenv("HOME", t.TempDir())
	prevDrv := drv
	drv = physicalLiveDriver{}
	t.Cleanup(func() { drv, physicalToolEnv = prevDrv, nil })

	devices, err := probePhysical(context.Background(), "")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	for _, d := range devices {
		t.Logf("probed %s %s id=%s serial=%s wired=%v", d.Platform, d.Name, d.ID, d.Serial, d.Wired)
		switch {
		case d.Platform == PlatformAndroid && d.Wired:
			pixel = d
		case d.Platform == PlatformIOS && d.Wired:
			iphone = d
		}
	}
	if pixel.ID == "" || iphone.ID == "" {
		t.Fatalf("the live run needs the Pixel and the iPhone plugged by cable, got %+v", devices)
	}
	seed(t, &State{Config: Config{IOS: 1, Android: 1}})
	return pixel, iphone
}

func physicalLease(t *testing.T, session string, platform Platform, appID string) (string, error) {
	t.Helper()
	var stderr strings.Builder
	id, err := Acquire(context.Background(), LeaseRequest{Session: session, Workdir: "/wd", Platform: platform, Type: RequirePhysical, AppID: appID}, &stderr)
	t.Logf("lease %s %s: %q %v %s", session, platform, id, err, stderr.String())
	return id, err
}

func liveStayOn(t *testing.T, p PhysicalDevice) string {
	t.Helper()
	v, err := readStayOn(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func liveApps(t *testing.T, p PhysicalDevice) []string {
	t.Helper()
	ids, err := installedApps(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(ids)
	return ids
}

func liveRun(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := runBounded(context.Background(), name, args...)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// An install outlasts cmdTimeout: a debug APK weighs about 200 MB.
func liveInstall(t *testing.T, name string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = physicalToolEnv
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

func assertNoPendingRestore(t *testing.T) {
	t.Helper()
	if r := load(t).Restores; len(r) != 0 {
		t.Fatalf("every restore must have succeeded, pending %+v", r)
	}
}

// Runs on the Pixel and the iPhone plugged by cable, with no virtual device
// on, on a slot nobody else needs. ATELIERFIT_STORE_APP names an unimportant
// Store app installed on the Pixel; ATELIERFIT_APK and ATELIERFIT_IOS_APP are
// debug builds of ATELIERFIT_APP_ID and ATELIERFIT_IOS_APP_ID (default: the
// same id), absent from their device before the run; the iOS build is
// optional.
func TestPhysicalLive(t *testing.T) {
	storeApp, apk, iosApp := os.Getenv("ATELIERFIT_STORE_APP"), os.Getenv("ATELIERFIT_APK"), os.Getenv("ATELIERFIT_IOS_APP")
	fitApp, fitIOSApp := os.Getenv("ATELIERFIT_APP_ID"), os.Getenv("ATELIERFIT_IOS_APP_ID")
	if fitIOSApp == "" {
		fitIOSApp = fitApp
	}
	if storeApp == "" || apk == "" || fitApp == "" || storeApp == "com.operationnezrouge.app" {
		t.Fatal("set ATELIERFIT_STORE_APP (never com.operationnezrouge.app), ATELIERFIT_APP_ID and ATELIERFIT_APK")
	}
	pixel, iphone := physicalLiveSetup(t)

	stayOnBefore := liveStayOn(t, pixel)
	pixelApps, iphoneApps := liveApps(t, pixel), liveApps(t, iphone)
	t.Logf("stay_on before the run: %s; %d Pixel packages, %d iPhone apps", stayOnBefore, len(pixelApps), len(iphoneApps))
	if slices.Contains(pixelApps, fitApp) || slices.Contains(iphoneApps, fitIOSApp) {
		t.Fatalf("%s and %s must be absent before the run", fitApp, fitIOSApp)
	}
	t.Cleanup(func() {
		for _, s := range []string{"live-ios", "live-android", "live-kill", "live-conflict"} {
			_ = Release(context.Background(), s, "")
		}
		if v, err := readStayOn(context.Background(), pixel.ID); err == nil && v != stayOnBefore {
			_ = writeStayOn(context.Background(), pixel.ID, stayOnBefore)
			t.Errorf("cleanup had to put stay_on back to %s (was %s)", stayOnBefore, v)
		}
	})

	t.Run("physical required returns the iPhone and the Pixel", func(t *testing.T) {
		if id, err := physicalLease(t, "live-ios", PlatformIOS, fitIOSApp); err != nil || id != iphone.ID {
			t.Fatalf("got %q %v, want %s", id, err, iphone.ID)
		}
		if id, err := physicalLease(t, "live-android", PlatformAndroid, fitApp); err != nil || id != pixel.ID {
			t.Fatalf("got %q %v, want %s", id, err, pixel.ID)
		}
		if got := liveStayOn(t, pixel); got != "3" {
			t.Fatalf("stay_on during the lease = %s, want 3", got)
		}
	})

	t.Run("iPhone pause touches nothing", func(t *testing.T) {
		if err := Pause(context.Background(), "live-ios"); err != nil {
			t.Fatal(err)
		}
		if got := liveApps(t, iphone); !slices.Equal(got, iphoneApps) {
			t.Fatalf("pause changed the iPhone's apps: %v", got)
		}
		if l := load(t).FindLease("live-ios", PlatformIOS); l == nil || !l.Suspended() {
			t.Fatalf("the lease must be suspended, got %+v", l)
		}
		if id, err := physicalLease(t, "live-ios", PlatformIOS, fitIOSApp); err != nil || id != iphone.ID {
			t.Fatalf("the next lease must return the same iPhone, got %q %v", id, err)
		}
	})

	t.Run("debug builds installed during the lease", func(t *testing.T) {
		liveInstall(t, "adb", "-s", pixel.ID, "install", "-r", apk)
		if !slices.Contains(liveApps(t, pixel), fitApp) {
			t.Fatal("the APK must be installed")
		}
		if iosApp == "" {
			t.Log("no ATELIERFIT_IOS_APP: the iPhone uninstall rests on the fake driver")
			return
		}
		liveInstall(t, "xcrun", "devicectl", "device", "install", "app", "--device", iphone.ID, iosApp)
		if !slices.Contains(liveApps(t, iphone), fitIOSApp) {
			t.Fatal("the iOS app must be installed")
		}
	})

	t.Run("Pixel stay_on through pause, resume and return", func(t *testing.T) {
		if err := Pause(context.Background(), "live-android"); err != nil {
			t.Fatal(err)
		}
		if got := liveStayOn(t, pixel); got != stayOnBefore {
			t.Fatalf("after pause stay_on = %s, want %s", got, stayOnBefore)
		}
		if id, err := physicalLease(t, "live-android", PlatformAndroid, fitApp); err != nil || id != pixel.ID {
			t.Fatalf("resume got %q %v", id, err)
		}
		if got := liveStayOn(t, pixel); got != "3" {
			t.Fatalf("after resume stay_on = %s, want 3", got)
		}
		liveRun(t, "adb", "-s", pixel.ID, "reverse", "tcp:9091", "tcp:9091")
		if err := Release(context.Background(), "live-android", ""); err != nil {
			t.Fatal(err)
		}
		assertNoPendingRestore(t)
		if got := liveStayOn(t, pixel); got != stayOnBefore {
			t.Fatalf("after return stay_on = %s, want %s", got, stayOnBefore)
		}
		if got := liveRun(t, "adb", "-s", pixel.ID, "reverse", "--list"); got != "" {
			t.Fatalf("adb reverse --list must be empty, got %q", got)
		}
	})

	t.Run("return uninstalls the debug builds only", func(t *testing.T) {
		if err := Release(context.Background(), "live-ios", ""); err != nil {
			t.Fatal(err)
		}
		assertNoPendingRestore(t)
		if got := liveApps(t, pixel); !slices.Equal(got, pixelApps) {
			t.Fatalf("the Pixel's packages must be back to the pre-lease list, diff %v", symmetricDiff(got, pixelApps))
		}
		if got := liveApps(t, iphone); !slices.Equal(got, iphoneApps) {
			t.Fatalf("the iPhone's apps must be back to the pre-lease list, diff %v", symmetricDiff(got, iphoneApps))
		}
	})

	t.Run("unreleased lease restored at the TTL reap", func(t *testing.T) {
		if _, err := physicalLease(t, "live-kill", PlatformAndroid, fitApp); err != nil {
			t.Fatal(err)
		}
		if got := liveStayOn(t, pixel); got != "3" {
			t.Fatalf("stay_on = %s, want 3", got)
		}
		later := time.Now().Add(TTL + time.Minute)
		clock = func() time.Time { return later }
		defer func() { clock = time.Now }()
		if err := Reap(context.Background()); err != nil {
			t.Fatal(err)
		}
		assertNoPendingRestore(t)
		if load(t).FindLease("live-kill", PlatformAndroid) != nil {
			t.Fatal("the unrenewed lease must be reaped")
		}
		if got := liveStayOn(t, pixel); got != stayOnBefore {
			t.Fatalf("after the reap stay_on = %s, want %s", got, stayOnBefore)
		}
		if got := liveRun(t, "adb", "-s", pixel.ID, "reverse", "--list"); got != "" {
			t.Fatalf("adb reverse --list must be empty, got %q", got)
		}
	})

	t.Run("the Store app makes the Pixel unavailable", func(t *testing.T) {
		if !slices.Contains(liveApps(t, pixel), storeApp) {
			t.Fatalf("%s must be installed on the Pixel", storeApp)
		}
		var stderr strings.Builder
		_, err := Acquire(context.Background(), LeaseRequest{Session: "live-conflict", Workdir: "/wd", Platform: PlatformAndroid, Type: RequirePhysical, AppID: storeApp}, &stderr)
		t.Logf("stderr: %s", stderr.String())
		if !errors.Is(err, ErrExhausted) {
			t.Fatalf("want exit 10, got %v", err)
		}
		if !strings.Contains(stderr.String(), "already installed and is neither a developer build nor named [D]") {
			t.Fatalf("stderr must say why, got %q", stderr.String())
		}
		if !slices.Contains(liveApps(t, pixel), storeApp) || liveStayOn(t, pixel) != stayOnBefore {
			t.Fatal("the Store app stays installed and nothing is touched")
		}
		if s := load(t); len(s.Leases) != 0 || len(s.Restores) != 0 {
			t.Fatalf("no lease or restore is left, got %+v %+v", s.Leases, s.Restores)
		}
	})
}

func symmetricDiff(a, b []string) []string {
	var out []string
	for _, x := range a {
		if !slices.Contains(b, x) {
			out = append(out, "+"+x)
		}
	}
	for _, x := range b {
		if !slices.Contains(a, x) {
			out = append(out, "-"+x)
		}
	}
	return out
}
