package devicebank

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/paths"
)

var (
	pixel  = PhysicalDevice{ID: "P7SERIAL", Serial: "P7SERIAL", Name: "Pixel 7 Pro", Platform: PlatformAndroid, Wired: true}
	iphone = PhysicalDevice{ID: "00008101-0011649C3EB8001E", Serial: "00008101-0011649C3EB8001E", Name: "iPhone 12 Mini", Platform: PlatformIOS, Wired: true}
)

const runApp = "com.example.app.development"

func acquireAs(session string, platform Platform, typ Requirement, appID string) (id, stderr string, err error) {
	var out strings.Builder
	id, err = Acquire(context.Background(), LeaseRequest{Session: session, Workdir: "/wd", Platform: platform, Type: typ, AppID: appID}, &out)
	return id, out.String(), err
}

func leaseAny(t *testing.T, session string, platform Platform) string {
	t.Helper()
	id, stderr, err := acquireAs(session, platform, RequireAny, runApp)
	if err != nil {
		t.Fatalf("lease --type any %s %s: %v (%s)", session, platform, err, stderr)
	}
	return id
}

func release(t *testing.T, session string) {
	t.Helper()
	if err := Release(context.Background(), session, ""); err != nil {
		t.Fatal(err)
	}
}

func emulator(i int) string { return androidDevice(i).TargetID() }

func TestAnyPrefersWiredPhysical(t *testing.T) {
	t.Run("iphone", func(t *testing.T) {
		f := useFake(t)
		f.plug(iphone)
		seed(t, bank(iosDevice(1)))
		if id := leaseAny(t, "sess-1", PlatformIOS); id != iphone.ID {
			t.Fatalf("rank 1 is the wired iPhone, got %q", id)
		}
		id, stderr, err := acquireAs("sess-2", PlatformIOS, RequireAny, runApp)
		if err != nil || id != "UDID-1" {
			t.Fatalf("the iPhone leased by another session falls to a simulator through the queue, got %q %v", id, err)
		}
		if !strings.Contains(stderr, "iPhone 12 Mini ("+iphone.Serial+") skipped: leased by session sess-1") {
			t.Fatalf("the skipped iPhone is named, stderr=%q", stderr)
		}
	})
	t.Run("pixel", func(t *testing.T) {
		f := useFake(t)
		f.plug(pixel)
		seed(t, bank(androidDevice(1)))
		if id := leaseAny(t, "sess-1", PlatformAndroid); id != pixel.ID {
			t.Fatalf("plugged = available: the Pixel is leased, got %q", id)
		}
		if f.count("startAVD") != 0 || len(load(t).Queue) != 0 {
			t.Fatalf("a physical lease powers nothing on and queues nothing, got %v", f.calls)
		}
	})
}

func TestUnpluggedFallsToVirtual(t *testing.T) {
	useFake(t)
	seed(t, bank(androidDevice(1)))
	if id := leaseAny(t, "sess-1", PlatformAndroid); id != emulator(1) {
		t.Fatalf("Pixel unplugged: a virtual device through the queue, got %q", id)
	}
}

func TestWaitReprobesPhysicals(t *testing.T) {
	f := useFake(t)
	seed(t, bank(androidDevice(1)))
	lease(t, "sess-1", PlatformAndroid)

	got := make(chan string, 1)
	go func() {
		id, _ := Acquire(context.Background(), LeaseRequest{Session: "sess-2", Workdir: "/wd", Platform: PlatformAndroid, Type: RequireAny, AppID: runApp, Wait: 5 * time.Second}, discard{})
		got <- id
	}()
	time.Sleep(50 * time.Millisecond)
	if findWaiter(load(t), "sess-2", PlatformAndroid) == nil {
		t.Fatal("with the only AVD held, the caller waits in the queue")
	}
	f.plug(pixel)
	if id := <-got; id != pixel.ID {
		t.Fatalf("a Pixel plugged during the wait is taken, got %q", id)
	}
	if findWaiter(load(t), "sess-2", PlatformAndroid) != nil {
		t.Fatal("served by a physical device, the caller leaves the virtual queue")
	}
}

func TestNoTypeNeverPhysical(t *testing.T) {
	for _, typ := range []Requirement{"", RequireVirtual} {
		f := useFake(t)
		f.plug(pixel, iphone)
		seed(t, bank(iosDevice(1), androidDevice(1)))
		if err := SetMaxOn(2); err != nil {
			t.Fatal(err)
		}
		ios, _, err := acquireAs("sess-1", PlatformIOS, typ, runApp)
		if err != nil || ios != "UDID-1" {
			t.Fatalf("type %q: a simulator even with the iPhone free, got %q %v", typ, ios, err)
		}
		android, _, err := acquireAs("sess-1", PlatformAndroid, typ, runApp)
		if err != nil || android != emulator(1) {
			t.Fatalf("type %q: an AVD even with the Pixel free, got %q %v", typ, android, err)
		}
		if f.count("apps") != 0 || f.count("appInfo") != 0 || f.count("setStayOn") != 0 {
			t.Fatalf("type %q: no physical device is even inspected, got %v", typ, f.calls)
		}
	}
}

func TestPhysicalRequiredExits10WithoutVirtual(t *testing.T) {
	f := useFake(t)
	f.plug(PhysicalDevice{ID: "192.168.1.20:5555", Serial: pixel.Serial, Name: pixel.Name, Platform: PlatformAndroid})
	seed(t, bank(androidDevice(1)))
	_, stderr, err := acquireAs("sess-1", PlatformAndroid, RequirePhysical, runApp)
	if !errors.Is(err, ErrExhausted) {
		t.Fatalf("physical required, none plugged by cable: exit 10, got %v", err)
	}
	s := load(t)
	if f.count("startAVD") != 0 || len(s.Leases) != 0 || len(s.Queue) != 0 {
		t.Fatalf("no virtual device is powered on or queued for, got %v leases %v queue %v", f.calls, s.Leases, s.Queue)
	}
	if !strings.Contains(stderr, "skipped: reachable without cable only") {
		t.Fatalf("the reason the Pixel was skipped is printed, stderr=%q", stderr)
	}
}

func TestAnyWithoutAppIDSkipsPhysical(t *testing.T) {
	useFake(t).plug(pixel)
	seed(t, bank(androidDevice(1)))
	id, stderr, err := acquireAs("sess-1", PlatformAndroid, RequireAny, "")
	if err != nil || id != emulator(1) {
		t.Fatalf("without --app-id the physical device is not leased, got %q %v", id, err)
	}
	if !strings.Contains(stderr, "no --app-id given") {
		t.Fatalf("stderr says why, got %q", stderr)
	}
}

func TestParseAdbDevicesLong(t *testing.T) {
	out := []byte(`* daemon started successfully
List of devices attached
P7SERIAL               device usb:1-1 product:cheetah model:Pixel_7_Pro device:cheetah transport_id:3
192.168.1.20:5555      device product:cheetah model:Pixel_7_Pro device:cheetah transport_id:4
adb-P7SERIAL-AbCdEf._adb-tls-connect._tcp device product:cheetah model:Pixel_7_Pro device:cheetah transport_id:5
emulator-5560          device product:sdk_gphone64_arm64 model:sdk_gphone64_arm64 transport_id:6
R5CT1234               unauthorized usb:1-2 transport_id:7
OFFLINE1               offline usb:1-3 transport_id:8
`)
	got := parseAdbDevicesLong(out)
	want := []adbTransport{
		{id: "P7SERIAL", model: "Pixel 7 Pro", usb: true},
		{id: "192.168.1.20:5555", model: "Pixel 7 Pro"},
		{id: "adb-P7SERIAL-AbCdEf._adb-tls-connect._tcp", model: "Pixel 7 Pro"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if s := mdnsSerial("adb-P7SERIAL-AbCdEf._adb-tls-connect._tcp"); s != "P7SERIAL" {
		t.Fatalf("mdns serial = %q", s)
	}
	if s := mdnsSerial("192.168.1.20:5555"); s != "" {
		t.Fatalf("an IP transport has no mdns serial, got %q", s)
	}
}

func TestUnauthorizedIsNotAvailable(t *testing.T) {
	got := parseAdbDevicesLong([]byte("List of devices attached\nR5CT1234 unauthorized usb:1-2 transport_id:7\n"))
	if len(got) != 0 {
		t.Fatalf("a phone without debugging allowed on this machine is not available, got %+v", got)
	}
}

const devicectlList = `{"result":{"devices":[
 {"deviceProperties":{"name":"iPhone 12 Mini"},"hardwareProperties":{"platform":"iOS","udid":"00008101-0011649C3EB8001E"},
  "connectionProperties":{"pairingState":"paired","transportType":"wired","tunnelState":"disconnected"}},
 {"deviceProperties":{"name":"Old iPad"},"hardwareProperties":{"platform":"iOS","udid":"00008027-AAAA"},
  "connectionProperties":{"pairingState":"paired","transportType":"wired","tunnelState":"unavailable"}},
 {"deviceProperties":{"name":"Watch"},"hardwareProperties":{"platform":"watchOS","udid":"00008301-BBBB"},
  "connectionProperties":{"pairingState":"paired","transportType":"localNetwork","tunnelState":"disconnected"}}
]}}`

func TestIOSWiredFromIoreg(t *testing.T) {
	plugged := []byte(`  | |   "USB Product Name" = "iPhone"
  | |   "USB Serial Number" = "000081010011649c3eb8001e"`)
	unplugged := []byte(`  | |   "USB Serial Number" = "167E39553137"`)

	got, err := parseIOSPhysical([]byte(devicectlList), plugged)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != iphone {
		t.Fatalf("the paired iPhone on the bus is wired, got %+v", got)
	}
	got, err = parseIOSPhysical([]byte(devicectlList), unplugged)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Wired {
		t.Fatalf("devicectl's cached wired transport does not make it wired, got %+v", got)
	}
}

func TestParseAppSignals(t *testing.T) {
	if ids := parsePmPackages("package:com.android.vending\npackage:ca.valian.scaffold\n\n"); !slices.Equal(ids, []string{"com.android.vending", "ca.valian.scaffold"}) {
		t.Fatalf("pm list packages = %v", ids)
	}
	for _, tt := range []struct {
		dump string
		want bool
	}{
		{"    pkgFlags=[ HAS_CODE ALLOW_CLEAR_USER_DATA DEBUGGABLE ALLOW_BACKUP ]", true},
		{"    pkgFlags=[ HAS_CODE ALLOW_CLEAR_USER_DATA ALLOW_BACKUP ]", false},
		{"    pkgFlags=[ HAS_CODE NOT_DEBUGGABLE_X ]", false},
	} {
		if got, err := parseDebuggable(tt.dump); err != nil || got != tt.want {
			t.Errorf("parseDebuggable(%q) = %v %v", tt.dump, got, err)
		}
	}
	if _, err := parseDebuggable("Unable to find package"); err == nil {
		t.Error("no pkgFlags is unreadable, not a release build")
	}
	label, err := parseApplicationLabel("package: name='ca.valian.app'\napplication-label:'[D] Nez rouge'\napplication-label-fr:'[D] Nez rouge FR'\n")
	if err != nil || label != "[D] Nez rouge" {
		t.Fatalf("label = %q %v", label, err)
	}
	if !(AppInfo{Installed: true, Name: "[D] Nez rouge"}).overwritable() || (AppInfo{Installed: true, Name: "Nez rouge"}).overwritable() {
		t.Fatal("only a developer build or a [D] name may be overwritten")
	}
}

func TestWirelessOnlySkippedWithNote(t *testing.T) {
	for _, tt := range []struct {
		platform Platform
		dev      PhysicalDevice
		virtual  string
	}{
		{PlatformAndroid, PhysicalDevice{ID: "adb-P7SERIAL-AbCdEf._adb-tls-connect._tcp", Serial: pixel.Serial, Name: pixel.Name, Platform: PlatformAndroid}, emulator(1)},
		{PlatformIOS, PhysicalDevice{ID: iphone.ID, Serial: iphone.Serial, Name: iphone.Name, Platform: PlatformIOS}, "UDID-1"},
	} {
		useFake(t).plug(tt.dev)
		seed(t, bank(iosDevice(1), androidDevice(1)))
		id, stderr, err := acquireAs("sess-1", tt.platform, RequireAny, runApp)
		if err != nil || id != tt.virtual {
			t.Fatalf("%s reachable without cable only: a virtual device, got %q %v", tt.dev.Name, id, err)
		}
		if !strings.Contains(stderr, tt.dev.Name+" ("+tt.dev.Serial+") skipped: reachable without cable only") {
			t.Fatalf("stderr says the device had no cable, got %q", stderr)
		}
	}
}

func TestConflictFallsToNextRank(t *testing.T) {
	f := useFake(t)
	f.plug(pixel)
	f.stayOnVal[pixel.Serial] = "0"
	f.install(pixel.Serial, runApp, AppInfo{Name: "Example"})
	seed(t, bank(androidDevice(1)))

	id, stderr, err := acquireAs("sess-1", PlatformAndroid, RequireAny, runApp)
	if err != nil || id != emulator(1) {
		t.Fatalf("the Store app makes the Pixel unavailable: a virtual device, got %q %v", id, err)
	}
	if !strings.Contains(stderr, runApp+" already installed and is neither a developer build nor named [D]") {
		t.Fatalf("stderr says why, got %q", stderr)
	}
	if f.count("setStayOn") != 0 || f.count("uninstall") != 0 || f.count("apps") != 0 || f.count("removeReverses") != 0 {
		t.Fatalf("nothing is touched on the Pixel, got %v", f.calls)
	}
	if !f.hasApp(pixel.Serial, runApp) || f.stayOnOf(pixel.Serial) != "0" {
		t.Fatal("the Store app stays installed, the screen setting unchanged")
	}
	s := load(t)
	if s.leaseForPhysical(&pixel) != nil || len(s.Restores) != 0 {
		t.Fatalf("no lease and no restore are left on the Pixel, got %+v %+v", s.Leases, s.Restores)
	}

	release(t, "sess-1")
	if _, _, err := acquireAs("sess-2", PlatformAndroid, RequirePhysical, runApp); !errors.Is(err, ErrExhausted) {
		t.Fatalf("under physical required the conflict exits 10, got %v", err)
	}
}

func TestDeveloperBuildIsOverwritable(t *testing.T) {
	f := useFake(t)
	f.plug(pixel)
	f.install(pixel.Serial, runApp, AppInfo{Developer: true})
	seed(t, bank(androidDevice(1)))
	if id := leaseAny(t, "sess-1", PlatformAndroid); id != pixel.ID {
		t.Fatalf("a developer build may be overwritten, got %q", id)
	}
	if f.count("androidLabel") != 0 {
		t.Fatalf("a debuggable build needs no label, got %v", f.calls)
	}
}

func TestDNamedIsOverwritable(t *testing.T) {
	for _, dev := range []PhysicalDevice{pixel, iphone} {
		f := useFake(t)
		f.plug(dev)
		f.install(dev.Serial, runApp, AppInfo{Name: "[D] Example"})
		seed(t, bank(iosDevice(1), androidDevice(1)))
		if id := leaseAny(t, "sess-1", dev.Platform); id != dev.ID {
			t.Fatalf("%s: a release build named [D] may be overwritten, got %q", dev.Name, id)
		}
	}
}

func TestUnreadableSignalIsConflict(t *testing.T) {
	for _, op := range []string{"appInfo", "androidLabel"} {
		f := useFake(t)
		f.plug(pixel)
		f.install(pixel.Serial, runApp, AppInfo{Name: "[D] Example"})
		f.fail[op] = 1
		seed(t, bank(androidDevice(1)))
		id, stderr, err := acquireAs("sess-1", PlatformAndroid, RequireAny, runApp)
		if err != nil || id != emulator(1) {
			t.Fatalf("%s unreadable: the device is unavailable, got %q %v", op, id, err)
		}
		if !strings.Contains(stderr, "skipped: could not read "+runApp) {
			t.Fatalf("%s: stderr says why, got %q", op, stderr)
		}
	}
}

func TestOverwritableNeverUninstalled(t *testing.T) {
	for _, dev := range []PhysicalDevice{pixel, iphone} {
		f := useFake(t)
		f.plug(dev)
		f.install(dev.Serial, runApp, AppInfo{Developer: true, Name: "[D] Example"})
		seed(t, bank(iosDevice(1), androidDevice(1)))
		leaseAny(t, "sess-1", dev.Platform)
		release(t, "sess-1")
		if !f.hasApp(dev.Serial, runApp) || f.count("uninstall") != 0 {
			t.Fatalf("%s: the app present before the lease stays, got %v", dev.Name, f.calls)
		}
	}
}

func TestStayOnWriteAheadAndRestore(t *testing.T) {
	f := useFake(t)
	f.plug(pixel)
	f.stayOnVal[pixel.Serial] = "0"
	seed(t, bank(androidDevice(1)))

	leaseAny(t, "sess-1", PlatformAndroid)
	l := load(t).FindLease("sess-1", PlatformAndroid)
	if l.Snapshot == nil || l.Snapshot.StayOnBefore == nil || *l.Snapshot.StayOnBefore != "0" || l.AppID != runApp {
		t.Fatalf("the value before the lease is recorded, got %+v", l)
	}
	if got := f.stayOnOf(pixel.Serial); got != "3" {
		t.Fatalf("stay_on is 3 during the lease, got %q", got)
	}
	read := slices.Index(f.calls, "stayOn "+pixel.ID)
	set := slices.Index(f.calls, "setStayOn "+pixel.ID+" 3")
	if read < 0 || set < read {
		t.Fatalf("the value is read before it is changed, got %v", f.calls)
	}

	f.reverses[pixel.Serial] = 2
	release(t, "sess-1")
	if got := f.stayOnOf(pixel.Serial); got != "0" || f.reverses[pixel.Serial] != 0 {
		t.Fatalf("the return restores stay_on and removes the reverses, got %q %d", got, f.reverses[pixel.Serial])
	}
	if s := load(t); len(s.Restores) != 0 {
		t.Fatalf("a successful restore leaves nothing pending, got %+v", s.Restores)
	}
}

func TestStayOnFailureGivesTheDeviceBack(t *testing.T) {
	f := useFake(t)
	f.plug(pixel)
	f.stayOnVal[pixel.Serial] = "0"
	f.fail["setStayOn"] = 1
	seed(t, bank(androidDevice(1)))

	id, stderr, err := acquireAs("sess-1", PlatformAndroid, RequireAny, runApp)
	if err != nil || id != emulator(1) {
		t.Fatalf("a refused stay_on falls to the next rank, got %q %v", id, err)
	}
	if !strings.Contains(stderr, "could not keep its screen on") {
		t.Fatalf("stderr says why, got %q", stderr)
	}
	if got, s := f.stayOnOf(pixel.Serial), load(t); got != "0" || len(s.Restores) != 0 {
		t.Fatalf("the recorded value is written back at once, got %q pending %+v", got, s.Restores)
	}
}

func TestPauseRestoresStayOnResumeReapplies(t *testing.T) {
	f := useFake(t)
	f.plug(pixel)
	f.stayOnVal[pixel.Serial] = "0"
	seed(t, bank(androidDevice(1)))

	leaseAny(t, "sess-1", PlatformAndroid)
	pause(t, "sess-1")
	if got := f.stayOnOf(pixel.Serial); got != "0" {
		t.Fatalf("pause gives back the value from before the lease, got %q", got)
	}
	if l := load(t).FindLease("sess-1", PlatformAndroid); !l.Suspended() {
		t.Fatal("the lease is suspended")
	}
	if f.count("removeReverses") != 0 || f.count("uninstall") != 0 {
		t.Fatalf("pause touches neither reverses nor apps, got %v", f.calls)
	}
	if id := leaseAny(t, "sess-1", PlatformAndroid); id != pixel.ID {
		t.Fatalf("resume returns the same Pixel, got %q", id)
	}
	if got := f.stayOnOf(pixel.Serial); got != "3" || load(t).FindLease("sess-1", PlatformAndroid).Suspended() {
		t.Fatalf("resume sets 3 again, got %q", got)
	}
	if f.count("stayOn ") != 1 {
		t.Fatalf("resume never takes a new snapshot, got %v", f.calls)
	}
	release(t, "sess-1")
	if got := f.stayOnOf(pixel.Serial); got != "0" {
		t.Fatalf("the return gives back the value from before the lease, got %q", got)
	}
}

func TestNullStayOnRestoredByDelete(t *testing.T) {
	f := useFake(t)
	f.plug(pixel)
	seed(t, bank(androidDevice(1)))
	leaseAny(t, "sess-1", PlatformAndroid)
	release(t, "sess-1")
	if got := f.stayOnOf(pixel.Serial); got != "null" || f.count("setStayOn "+pixel.ID+" null") != 1 {
		t.Fatalf("an unset key is deleted again, got %q %v", got, f.calls)
	}
}

func TestTTLReapLeavesRestorePending(t *testing.T) {
	f := useFake(t)
	c := useClock(t, t0)
	f.plug(pixel)
	f.stayOnVal[pixel.Serial] = "0"
	seed(t, bank(androidDevice(1)))
	leaseAny(t, "sess-1", PlatformAndroid)

	f.plug()
	c.advance(TTL + time.Minute)
	if err := Reap(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := load(t)
	if s.FindLease("sess-1", PlatformAndroid) != nil || len(s.Restores) != 1 {
		t.Fatalf("the expired lease becomes a pending restore, got %+v %+v", s.Leases, s.Restores)
	}
	if r := s.Restores[0]; r.LastError == "" || r.Reason != "ttl" {
		t.Fatalf("an unreachable device keeps its restore pending with the reason, got %+v", r)
	}
}

func TestPendingRestoreRetriedWhenReachable(t *testing.T) {
	f := useFake(t)
	c := useClock(t, t0)
	f.plug(pixel)
	f.stayOnVal[pixel.Serial] = "0"
	seed(t, bank(androidDevice(1)))
	leaseAny(t, "sess-1", PlatformAndroid)
	f.plug()
	OnEmit("sess-1", true)
	if s := load(t); len(s.Restores) != 1 {
		t.Fatalf("session end with the Pixel away leaves a pending restore, got %+v", s.Restores)
	}

	c.advance(time.Minute)
	wifi := PhysicalDevice{ID: "192.168.1.20:5555", Serial: pixel.Serial, Name: pixel.Name, Platform: PlatformAndroid}
	f.plug(wifi)
	if err := Reap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := load(t); len(s.Restores) != 1 {
		t.Fatalf("a restore is not retried before its retry delay, got %+v", s.Restores)
	}

	c.advance(2 * time.Minute)
	if err := Reap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := load(t); len(s.Restores) != 0 {
		t.Fatalf("the reachable Pixel is restored at the next reap, got %+v", s.Restores)
	}
	if got := f.stayOnOf(pixel.Serial); got != "0" || f.count("setStayOn "+wifi.ID+" 0") != 1 {
		t.Fatalf("the restore goes through adb wifi, got %q %v", got, f.calls)
	}
}

func TestPendingRestoreBlocksLease(t *testing.T) {
	f := useFake(t)
	useClock(t, t0)
	f.plug(pixel)
	s := bank(androidDevice(1))
	s.Restores = []*PendingRestore{{DeviceID: pixel.ID, Serial: pixel.Serial, Name: pixel.Name, Platform: PlatformAndroid, EndedAt: t0, StartedAt: t0}}
	seed(t, s)

	id, stderr, err := acquireAs("sess-1", PlatformAndroid, RequireAny, runApp)
	if err != nil || id != emulator(1) {
		t.Fatalf("a device owed its restore is not leasable, got %q %v", id, err)
	}
	if !strings.Contains(stderr, "skipped: restore pending since") {
		t.Fatalf("stderr says why, got %q", stderr)
	}
}

func TestCrashBeforeSnapshotRemovesReversesOnly(t *testing.T) {
	f := useFake(t)
	f.plug(pixel)
	f.install(pixel.Serial, "com.user.app", AppInfo{})
	f.install(pixel.Serial, runApp, AppInfo{Developer: true})
	s := bank(androidDevice(1))
	commitLease(s, &Candidate{Physical: &pixel}, "sess-1", "/wd", PlatformAndroid, time.Now())
	seed(t, s)

	release(t, "sess-1")
	if f.count("removeReverses") != 1 || f.count("setStayOn") != 0 || f.count("uninstall") != 0 || f.count("apps") != 0 {
		t.Fatalf("without a snapshot only the reverses are removed, got %v", f.calls)
	}
	if len(load(t).Restores) != 0 {
		t.Fatal("the restore succeeded")
	}
}

func TestPauseIOSPhysicalTouchesNothing(t *testing.T) {
	f := useFake(t)
	f.plug(iphone)
	f.install(iphone.Serial, runApp, AppInfo{Developer: true})
	seed(t, bank(iosDevice(1)))
	leaseAny(t, "sess-1", PlatformIOS)

	f.resetCalls()
	pause(t, "sess-1")
	if len(f.calls) != 0 {
		t.Fatalf("pausing an iPhone touches nothing, got %v", f.calls)
	}
	if l := load(t).FindLease("sess-1", PlatformIOS); !l.Suspended() {
		t.Fatal("the lease is suspended")
	}
	if id := leaseAny(t, "sess-1", PlatformIOS); id != iphone.ID {
		t.Fatalf("the next lease returns the same iPhone, got %q", id)
	}
	if len(f.calls) != 0 || !f.hasApp(iphone.Serial, runApp) {
		t.Fatalf("resuming an iPhone touches nothing, got %v", f.calls)
	}
}

func TestVanishedDuringPauseChoosesAgain(t *testing.T) {
	f := useFake(t)
	f.plug(pixel)
	f.stayOnVal[pixel.Serial] = "0"
	seed(t, bank(androidDevice(1)))
	leaseAny(t, "sess-1", PlatformAndroid)
	pause(t, "sess-1")
	f.plug()

	id, stderr, err := acquireAs("sess-1", PlatformAndroid, RequireAny, runApp)
	if err != nil || id != emulator(1) {
		t.Fatalf("the Pixel gone, the choice is made again, got %q %v", id, err)
	}
	if !strings.Contains(stderr, "Pixel 7 Pro (P7SERIAL) is no longer connected by cable — lease dropped, choosing again") {
		t.Fatalf("stderr says the lease was dropped, got %q", stderr)
	}
	if s := load(t); len(s.Restores) != 1 || s.Restores[0].Reason != "unplugged" {
		t.Fatalf("the dropped lease leaves a pending restore, got %+v", s.Restores)
	}
}

func TestRestoreUninstallsOnlyNewDeveloperBuilds(t *testing.T) {
	for _, dev := range []PhysicalDevice{pixel, iphone} {
		f := useFake(t)
		f.plug(dev)
		f.install(dev.Serial, "com.user.bank", AppInfo{Name: "Bank"})
		f.install(dev.Serial, "com.user.devtool", AppInfo{Developer: true})
		seed(t, bank(iosDevice(1), androidDevice(1)))
		leaseAny(t, dev.Serial, dev.Platform)

		f.install(dev.Serial, runApp, AppInfo{Developer: true})
		f.install(dev.Serial, "com.store.new", AppInfo{Name: "New"})
		release(t, dev.Serial)

		if f.hasApp(dev.Serial, runApp) {
			t.Fatalf("%s: the debug build installed during the lease is gone", dev.Name)
		}
		for _, kept := range []string{"com.user.bank", "com.user.devtool", "com.store.new"} {
			if !f.hasApp(dev.Serial, kept) {
				t.Fatalf("%s: %s must stay", dev.Name, kept)
			}
		}
	}
}

func TestDedupBySerialPrefersUSB(t *testing.T) {
	ts := []adbTransport{
		{id: "adb-P7SERIAL-AbCdEf._adb-tls-connect._tcp", model: "Pixel 7 Pro"},
		{id: "192.168.1.20:5555", model: "Pixel 7 Pro"},
		{id: "P7SERIAL", model: "Pixel 7 Pro", usb: true},
	}
	serialOf := func(id string) string {
		if id == "192.168.1.20:5555" || id == "P7SERIAL" {
			return "P7SERIAL"
		}
		return ""
	}
	got := groupBySerial(ts, serialOf)
	if len(got) != 1 || got[0] != pixel {
		t.Fatalf("one entry per serial, the USB transport targeted, got %+v", got)
	}

	f := useFake(t)
	f.plug(got...)
	seed(t, bank(androidDevice(1)))
	rows, _, err := StatusRows(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var physical []StatusRow
	for _, r := range rows {
		if r.Physical {
			physical = append(physical, r)
		}
	}
	if len(physical) != 1 || physical[0].Name != "Pixel 7 Pro" || physical[0].ID != "P7SERIAL" || physical[0].Serial != "P7SERIAL" {
		t.Fatalf("status lists the Pixel once by its USB transport, got %+v", physical)
	}
}

func TestNoDestructiveOpOnPhysical(t *testing.T) {
	for _, dev := range []PhysicalDevice{pixel, iphone} {
		t.Run(dev.Name, func(t *testing.T) {
			f := useFake(t)
			c := useClock(t, t0)
			f.plug(dev)
			f.install(dev.Serial, "com.user.app", AppInfo{})
			seed(t, bank(iosDevice(1), androidDevice(1)))

			leaseAny(t, "sess-lease", dev.Platform)
			pause(t, "sess-lease")
			leaseAny(t, "sess-lease", dev.Platform)
			release(t, "sess-lease")

			leaseAny(t, "sess-end", dev.Platform)
			OnEmit("sess-end", true)

			leaseAny(t, "sess-ttl", dev.Platform)
			c.advance(TTL + time.Minute)
			if err := Reap(context.Background()); err != nil {
				t.Fatal(err)
			}

			leaseAny(t, "sess-gone", dev.Platform)
			f.plug()
			leaseAny(t, "sess-gone", dev.Platform)
			c.advance(3 * time.Minute)
			f.plug(dev)
			if err := Reap(context.Background()); err != nil {
				t.Fatal(err)
			}
			release(t, "sess-gone")

			f.install(dev.Serial, runApp, AppInfo{Name: "Store"})
			leaseAny(t, "sess-conflict", dev.Platform)
			release(t, "sess-conflict")
			c.advance(3 * time.Minute)
			if err := Reap(context.Background()); err != nil {
				t.Fatal(err)
			}

			if len(load(t).Restores) != 0 || !f.hasApp(dev.Serial, "com.user.app") || !f.hasApp(dev.Serial, runApp) {
				t.Fatalf("every restore settled and the user's apps stayed, got %+v", load(t).Restores)
			}
		})
	}
}

func TestConcurrentPhysicalLeaseUnderFlock(t *testing.T) {
	useFake(t).plug(pixel)
	seed(t, bank(androidDevice(1)))

	ids := make([]string, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, session := range []string{"sess-a", "sess-b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids[i], _, errs[i] = acquireAs(session, PlatformAndroid, RequirePhysical, runApp)
		}()
	}
	wg.Wait()

	got := 0
	for i := range ids {
		switch {
		case ids[i] == pixel.ID:
			got++
		case !errors.Is(errs[i], ErrExhausted):
			t.Fatalf("the loser exits 10, got %q %v", ids[i], errs[i])
		}
	}
	if got != 1 || len(load(t).Leases) != 1 {
		t.Fatalf("exactly one session leases the Pixel, got %v %v", ids, errs)
	}
}

func TestPreviousStateWithPhysicalLeaseLoads(t *testing.T) {
	f := useFake(t)
	f.plug(pixel)
	now := time.Now().UTC().Format(time.RFC3339)
	legacy := `{"config":{"ios":0,"android":1,"maxOn":1},"devices":[
  {"name":"atelier-android-1","platform":"android","avd":"atelier-android-1","port":5560,"state":"off","lastUsedAt":"` + now + `"}],
"leases":[{"sessionId":"sess-1","platform":"android","deviceName":" Pixel 7 Pro (usb)","deviceId":"P7SERIAL","physical":true,"workdir":"/wd","acquiredAt":"` + now + `","renewedAt":"` + now + `"}]}`
	if err := paths.EnsureDir(paths.MustRoot()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Devices(), []byte(legacy), paths.FileMode); err != nil {
		t.Fatal(err)
	}

	if id := leaseAny(t, "sess-1", PlatformAndroid); id != pixel.ID {
		t.Fatalf("the in-flight physical lease is honored, got %q", id)
	}
	pause(t, "sess-1")
	if id := leaseAny(t, "sess-1", PlatformAndroid); id != pixel.ID || f.count("setStayOn") != 0 {
		t.Fatalf("without a recorded value the screen setting is never changed, got %q %v", id, f.calls)
	}
	release(t, "sess-1")
	if f.count("removeReverses") != 1 || f.count("setStayOn") != 0 || f.count("uninstall") != 0 {
		t.Fatalf("a lease without snapshot is restored by removing reverses only, got %v", f.calls)
	}
	var raw map[string]any
	data, _ := os.ReadFile(paths.Devices())
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["restores"]; ok {
		t.Fatalf("a settled restore leaves no restores key, got %s", data)
	}
}
