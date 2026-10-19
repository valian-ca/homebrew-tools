package devicebank

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDeferredRestoreKeepsTheRunsApp(t *testing.T) {
	f := useFake(t)
	c := useClock(t, t0)
	f.plug(pixel)
	seed(t, bank(androidDevice(1)))
	leaseAny(t, "sess-1", PlatformAndroid)
	f.install(pixel.Serial, runApp, AppInfo{Developer: true})

	f.plug()
	release(t, "sess-1")
	if len(load(t).Restores) != 1 {
		t.Fatalf("an unplugged Pixel keeps its restore pending, got %+v", load(t).Restores)
	}
	f.plug(pixel)
	c.advance(restoreReapAfter + time.Minute)
	if err := Reap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(load(t).Restores) != 0 || !f.hasApp(pixel.Serial, runApp) || f.count("removeReverses") != 1 {
		t.Fatalf("once back, the Pixel is restored with the run's debug build kept, got %+v %v", load(t).Restores, f.calls)
	}
}

func TestIPhoneRestoreNeedsNoDevice(t *testing.T) {
	f := useFake(t)
	f.plug(iphone)
	seed(t, bank(iosDevice(1)))
	leaseAny(t, "sess-1", PlatformIOS)

	f.plug()
	release(t, "sess-1")
	if len(load(t).Restores) != 0 {
		t.Fatalf("an iPhone has nothing to put back: its restore succeeds unplugged, got %+v", load(t).Restores)
	}
	f.plug(iphone)
	if id := leaseAny(t, "sess-2", PlatformIOS); id != iphone.ID {
		t.Fatalf("the iPhone is leasable as soon as it is back, got %q", id)
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
