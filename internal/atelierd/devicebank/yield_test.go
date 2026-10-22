package devicebank

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestQueuedSuspendedVirtualYieldsToPhysicalPluggedDuringWait(t *testing.T) {
	f := useFake(t)
	seed(t, bank(iosDevice(1), iosDevice(2)))
	leaseAny(t, "sess-1", PlatformIOS)
	pause(t, "sess-1")
	lease(t, "sess-2", PlatformIOS)

	got := make(chan string, 1)
	go func() {
		id, _ := Acquire(context.Background(), LeaseRequest{Session: "sess-1", Workdir: "/wd", Platform: PlatformIOS, Type: RequireAny, AppID: runApp, Wait: 5 * time.Second}, discard{})
		got <- id
	}()
	time.Sleep(50 * time.Millisecond)
	if findWaiter(load(t), "sess-1", PlatformIOS) == nil {
		t.Fatal("with the limit taken by sess-2, the suspended simulator waits in the queue")
	}
	f.plug(iphone)
	if id := <-got; id != iphone.ID {
		t.Fatalf("an iPhone plugged during the wait takes the suspended simulator's place, got %q", id)
	}
	s := load(t)
	if findWaiter(s, "sess-1", PlatformIOS) != nil || s.leaseForDevice("atelier-ios-1") != nil {
		t.Fatalf("the caller leaves the queue and the simulator lease ends, got queue %+v leases %+v", s.Queue, s.Leases)
	}
}

func TestVetSkipsConflictThenTakesNextPhysical(t *testing.T) {
	other := PhysicalDevice{ID: "IPHONE-2", Serial: "IPHONE-2", Name: "iPhone 15", Platform: PlatformIOS, Wired: true}
	f := useFake(t)
	seed(t, bank(iosDevice(1)))
	leaseAny(t, "sess-1", PlatformIOS)
	pause(t, "sess-1")
	f.plug(iphone, other)
	f.install(iphone.Serial, runApp, AppInfo{Name: "Example"})

	id, stderr, err := acquireAs("sess-1", PlatformIOS, RequireAny, runApp)
	if err != nil || id != other.ID {
		t.Fatalf("the conflicting iPhone is skipped and the next one taken, got %q %v (%s)", id, err, stderr)
	}
	if f.count("eraseIOS") != 1 {
		t.Fatalf("the simulator is recycled once, got %v", f.calls)
	}
}

func TestSuspendedVirtualYieldsToPluggedPhysical(t *testing.T) {
	for _, dev := range []PhysicalDevice{iphone, pixel} {
		t.Run(dev.Name, func(t *testing.T) {
			f := useFake(t)
			seed(t, bank(iosDevice(1), androidDevice(1)))
			virtual := leaseAny(t, "sess-1", dev.Platform)
			pause(t, "sess-1")
			f.plug(dev)

			id, stderr, err := acquireAs("sess-1", dev.Platform, RequireAny, runApp)
			if err != nil || id != dev.ID {
				t.Fatalf("the next wave's lease takes the %s plugged meanwhile, got %q %v (%s)", dev.Name, id, err, stderr)
			}
			if !strings.Contains(stderr, dev.Name+" ("+dev.Serial+") is plugged and free — the lease on ") {
				t.Fatalf("stderr says the virtual lease ended, got %q", stderr)
			}
			s := load(t)
			d := s.Devices[0]
			if dev.Platform == PlatformAndroid {
				d = s.Devices[1]
			}
			if d.TargetID() != virtual || s.leaseForDevice(d.Name) != nil || d.State != StateOff {
				t.Fatalf("the virtual device is recycled and free, got %+v", d)
			}
			if !strings.Contains(strings.Join(f.spawned, " "), d.Name) {
				t.Fatalf("its recycle was spawned, got %v", f.spawned)
			}
			if dev.Platform == PlatformIOS && f.count("eraseIOS") != 1 {
				t.Fatalf("the simulator is erased for the next session, got %v", f.calls)
			}
		})
	}
}

func TestPoweredOnVirtualKeptWhenPhysicalPlugged(t *testing.T) {
	f := useFake(t)
	seed(t, bank(iosDevice(1)))
	leaseAny(t, "sess-1", PlatformIOS)
	f.plug(iphone)
	if id := leaseAny(t, "sess-1", PlatformIOS); id != "UDID-1" {
		t.Fatalf("mid-wave, the powered-on simulator is kept, got %q", id)
	}
	if f.count("appInfo") != 0 || len(f.spawned) != 0 {
		t.Fatalf("the iPhone is not even inspected, nothing is recycled, got %v %v", f.calls, f.spawned)
	}
}

func TestSuspendedVirtualKeptWithoutAppIDOrUnderVirtual(t *testing.T) {
	for _, tt := range []struct {
		typ   Requirement
		appID string
	}{{RequireAny, ""}, {RequireVirtual, runApp}} {
		f := useFake(t)
		seed(t, bank(iosDevice(1)))
		lease(t, "sess-1", PlatformIOS)
		pause(t, "sess-1")
		f.plug(iphone)
		if id, _, err := acquireAs("sess-1", PlatformIOS, tt.typ, tt.appID); err != nil || id != "UDID-1" {
			t.Fatalf("type %q app %q: the suspended simulator is resumed, got %q %v", tt.typ, tt.appID, id, err)
		}
		if len(f.spawned) != 0 {
			t.Fatalf("type %q app %q: nothing is recycled, got %v", tt.typ, tt.appID, f.spawned)
		}
	}
}

func TestConflictingPhysicalKeepsSuspendedVirtual(t *testing.T) {
	f := useFake(t)
	seed(t, bank(iosDevice(1)))
	leaseAny(t, "sess-1", PlatformIOS)
	pause(t, "sess-1")
	f.plug(iphone)
	f.install(iphone.Serial, runApp, AppInfo{Name: "Example"})

	id, stderr, err := acquireAs("sess-1", PlatformIOS, RequireAny, runApp)
	if err != nil || id != "UDID-1" {
		t.Fatalf("an iPhone carrying a Store build leaves the simulator resumed, got %q %v", id, err)
	}
	if !strings.Contains(stderr, "skipped: "+runApp+" already installed") {
		t.Fatalf("stderr says why the iPhone was not taken, got %q", stderr)
	}
	if len(f.spawned) != 0 || f.count("eraseIOS") != 0 {
		t.Fatalf("the simulator keeps its data, got %v %v", f.spawned, f.calls)
	}
}

func TestVirtualTypeEndsHeldPhysical(t *testing.T) {
	f := useFake(t)
	f.plug(pixel)
	f.stayOnVal[pixel.Serial] = "0"
	seed(t, bank(androidDevice(1)))
	leaseAny(t, "sess-1", PlatformAndroid)

	id, stderr, err := acquireAs("sess-1", PlatformAndroid, RequireVirtual, runApp)
	if err != nil || id != emulator(1) {
		t.Fatalf("--type virtual always gives a virtual device, got %q %v", id, err)
	}
	if !strings.Contains(stderr, "--type virtual ends the lease on Pixel 7 Pro (P7SERIAL)") {
		t.Fatalf("stderr says the physical lease ended, got %q", stderr)
	}
	s := load(t)
	if s.leaseForPhysical(&pixel) != nil || len(s.Restores) != 0 || f.stayOnOf(pixel.Serial) != "0" {
		t.Fatalf("the Pixel is returned as release returns it, got leases %+v restores %+v stay_on %q", s.Leases, s.Restores, f.stayOnOf(pixel.Serial))
	}
}

func TestVirtualTypeNeverProbesPhysical(t *testing.T) {
	f := useFake(t)
	f.plug(pixel)
	seed(t, bank(androidDevice(1)))
	leaseAny(t, "sess-1", PlatformAndroid)
	f.plug()
	f.runSpawns = false
	f.probes = 0

	if id, _, err := acquireAs("sess-1", PlatformAndroid, RequireVirtual, runApp); err != nil || id != emulator(1) {
		t.Fatalf("--type virtual gives a virtual device, got %q %v", id, err)
	}
	if f.probes != 0 {
		t.Fatalf("under --type virtual no physical device is probed, not even the held one, got %d probes", f.probes)
	}
}

func TestVetReadFailureKeepsSuspendedVirtual(t *testing.T) {
	f := useFake(t)
	seed(t, bank(androidDevice(1)))
	leaseAny(t, "sess-1", PlatformAndroid)
	pause(t, "sess-1")
	f.plug(pixel)
	f.fail["apps"] = 1

	id, stderr, err := acquireAs("sess-1", PlatformAndroid, RequireAny, runApp)
	if err != nil || id != emulator(1) {
		t.Fatalf("a Pixel whose apps cannot be listed leaves the emulator resumed, got %q %v", id, err)
	}
	if !strings.Contains(stderr, "Pixel 7 Pro (P7SERIAL) skipped: could not list its apps") {
		t.Fatalf("stderr says why the Pixel was not taken, got %q", stderr)
	}
	if len(f.spawned) != 0 {
		t.Fatalf("the emulator keeps its data, got %v", f.spawned)
	}
}

func TestCancelDuringVetKeepsSuspendedVirtual(t *testing.T) {
	f := useFake(t)
	seed(t, bank(iosDevice(1)))
	leaseAny(t, "sess-1", PlatformIOS)
	pause(t, "sess-1")
	f.plug(iphone)

	ctx, cancel := context.WithCancel(context.Background())
	f.onAppInfo = cancel
	_, err := Acquire(ctx, LeaseRequest{Session: "sess-1", Workdir: "/wd", Platform: PlatformIOS, Type: RequireAny, AppID: runApp}, discard{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled vet returns the cancellation, got %v", err)
	}
	s := load(t)
	if l := s.FindLease("sess-1", PlatformIOS); l == nil || l.DeviceName != "atelier-ios-1" || !l.Suspended() {
		t.Fatalf("the suspended simulator lease is kept, got %+v", l)
	}
	if len(f.spawned) != 0 || findWaiter(s, "sess-1", PlatformIOS) != nil {
		t.Fatalf("nothing is recycled and no waiter is left, got %v %+v", f.spawned, s.Queue)
	}
	if id := leaseAny(t, "sess-1", PlatformIOS); id != iphone.ID {
		t.Fatalf("the iPhone was not excluded: the next lease takes it, got %q", id)
	}
}

func TestPhysicalTypeEndsHeldVirtual(t *testing.T) {
	f := useFake(t)
	seed(t, bank(iosDevice(1)))
	leaseAny(t, "sess-1", PlatformIOS)

	if _, _, err := acquireAs("sess-1", PlatformIOS, RequirePhysical, runApp); !errors.Is(err, ErrExhausted) {
		t.Fatalf("no iPhone plugged: exit 10, got %v", err)
	}
	if s := load(t); s.FindLease("sess-1", PlatformIOS) != nil || f.count("eraseIOS") != 1 {
		t.Fatalf("the simulator lease ended and its device was recycled, got %+v %v", s.Leases, f.calls)
	}

	f.plug(iphone)
	if id, _, err := acquireAs("sess-1", PlatformIOS, RequirePhysical, runApp); err != nil || id != iphone.ID {
		t.Fatalf("the plugged iPhone is leased, got %q %v", id, err)
	}
}
