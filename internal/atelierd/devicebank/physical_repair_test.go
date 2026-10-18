package devicebank

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDeferredRestoreUninstallsOnlyTheRunsApp(t *testing.T) {
	for _, dev := range []PhysicalDevice{pixel, iphone} {
		t.Run(dev.Name, func(t *testing.T) {
			f := useFake(t)
			c := useClock(t, t0)
			f.plug(dev)
			seed(t, bank(iosDevice(1), androidDevice(1)))
			leaseAny(t, "sess-1", dev.Platform)
			f.install(dev.Serial, runApp, AppInfo{Developer: true})
			f.install(dev.Serial, "com.other.during", AppInfo{Developer: true})

			f.plug()
			release(t, "sess-1")
			f.plug(dev)
			f.install(dev.Serial, "com.user.own.development", AppInfo{Developer: true})
			c.advance(restoreReapAfter + time.Minute)
			if err := Reap(context.Background()); err != nil {
				t.Fatal(err)
			}

			if len(load(t).Restores) != 0 || f.hasApp(dev.Serial, runApp) {
				t.Fatalf("the run's debug build is gone once the device is back, got %+v", load(t).Restores)
			}
			for _, kept := range []string{"com.other.during", "com.user.own.development"} {
				if !f.hasApp(dev.Serial, kept) {
					t.Fatalf("%s must stay: only the run's app is uninstalled", kept)
				}
			}
		})
	}
}

func TestRestoreWithoutAppIDUninstallsNothing(t *testing.T) {
	f := useFake(t)
	f.plug(pixel)
	f.install(pixel.Serial, runApp, AppInfo{Developer: true})
	s := bank(androidDevice(1))
	s.Restores = []*PendingRestore{{DeviceID: pixel.ID, Serial: pixel.Serial, Name: pixel.Name, Platform: PlatformAndroid,
		Snapshot: &PhysicalSnapshot{AppsBefore: []string{}}, EndedAt: t0, StartedAt: t0}}
	seed(t, s)

	if err := RunRestore(context.Background(), pixel.ID); err != nil {
		t.Fatal(err)
	}
	if !f.hasApp(pixel.Serial, runApp) || f.count("uninstall") != 0 || len(load(t).Restores) != 0 {
		t.Fatalf("a restore written before the run's app was recorded uninstalls nothing, got %v", f.calls)
	}
}

func TestRestoreSkipsAStayOnAlreadyBack(t *testing.T) {
	f := useFake(t)
	f.plug(pixel)
	f.stayOnVal[pixel.Serial] = "0"
	seed(t, bank(androidDevice(1)))
	leaseAny(t, "sess-1", PlatformAndroid)
	pause(t, "sess-1")

	f.fail["setStayOn"] = 99
	release(t, "sess-1")
	if s := load(t); len(s.Restores) != 0 || f.stayOnOf(pixel.Serial) != "0" {
		t.Fatalf("a device already holding its value is restored without a write, got %+v", s.Restores)
	}
}

func TestRestoreRefusedStaysPendingUntilForgotten(t *testing.T) {
	f := useFake(t)
	f.plug(pixel)
	f.stayOnVal[pixel.Serial] = "0"
	seed(t, bank(androidDevice(1)))
	leaseAny(t, "sess-1", PlatformAndroid)

	f.fail["setStayOn"] = 99
	release(t, "sess-1")
	if s := load(t); len(s.Restores) != 1 || !strings.Contains(s.Restores[0].LastError, "setStayOn") {
		t.Fatalf("a refused write keeps the restore pending, got %+v", s.Restores)
	}
	if _, _, err := acquireAs("sess-2", PlatformAndroid, RequirePhysical, runApp); err == nil {
		t.Fatal("the device owed its restore is not leasable")
	}

	if err := ForgetRestore(pixel.Serial); err != nil {
		t.Fatal(err)
	}
	f.fail["setStayOn"] = 0
	if id := leaseAny(t, "sess-2", PlatformAndroid); id != pixel.ID {
		t.Fatalf("a forgotten restore makes the device leasable again, got %q", id)
	}
	if err := ForgetRestore("unknown"); err == nil || !strings.Contains(err.Error(), `no pending restore for "unknown"`) {
		t.Fatalf("forgetting nothing is an error, got %v", err)
	}
}

func TestReturnDuringStayOnWriteLeavesValueBack(t *testing.T) {
	for _, tt := range []struct {
		name  string
		setup func(t *testing.T)
	}{
		{"take", func(t *testing.T) {}},
		{"resume", func(t *testing.T) {
			leaseAny(t, "sess-1", PlatformAndroid)
			pause(t, "sess-1")
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := useFake(t)
			f.plug(pixel)
			f.stayOnVal[pixel.Serial] = "0"
			seed(t, bank(androidDevice(1)))
			tt.setup(t)
			f.onStayOn = func(value string) {
				if value == "3" {
					release(t, "sess-1")
				}
			}

			if _, _, err := acquireAs("sess-1", PlatformAndroid, RequireAny, runApp); !errors.Is(err, ErrLeaseLost) {
				t.Fatalf("a lease returned while its screen setting is written hands back no device, got %v", err)
			}
			s := load(t)
			if got := f.stayOnOf(pixel.Serial); got != "0" || len(s.Restores) != 0 || len(s.Leases) != 0 {
				t.Fatalf("the value from before the lease wins, got stay_on %q restores %+v leases %+v", got, s.Restores, s.Leases)
			}
		})
	}
}

func TestPauseDuringStayOnWriteLeavesValueBack(t *testing.T) {
	f := useFake(t)
	f.plug(pixel)
	f.stayOnVal[pixel.Serial] = "0"
	seed(t, bank(androidDevice(1)))
	f.onStayOn = func(value string) {
		if value == "3" {
			pause(t, "sess-1")
		}
	}

	if id := leaseAny(t, "sess-1", PlatformAndroid); id != pixel.ID {
		t.Fatalf("a lease paused while it is prepared is still handed out, got %q", id)
	}
	if l := load(t).FindLease("sess-1", PlatformAndroid); !l.Suspended() || f.stayOnOf(pixel.Serial) != "0" {
		t.Fatalf("the pause keeps its value, got %+v stay_on %q", l, f.stayOnOf(pixel.Serial))
	}
}
