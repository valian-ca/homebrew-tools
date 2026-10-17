package devicebank

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/paths"
)

func pause(t *testing.T, session string) {
	t.Helper()
	if err := Pause(context.Background(), session); err != nil {
		t.Fatal(err)
	}
}

func TestPauseSuspendsAndFreesSlot(t *testing.T) {
	f := useFake(t)
	seed(t, bank(iosDevice(1), iosDevice(2)))
	lease(t, "sess-a", PlatformIOS)
	if err := leaseErr("sess-b", PlatformIOS); !errors.Is(err, ErrExhausted) {
		t.Fatalf("the default limit of one blocks a second device, got %v", err)
	}

	pause(t, "sess-a")
	s := load(t)
	l := s.FindLease("sess-a", PlatformIOS)
	if l == nil || !l.Suspended() || f.isOn("UDID-1") || s.FindDevice("atelier-ios-1").State != StateOff {
		t.Fatalf("pause must power off and keep a suspended lease, got lease %+v", l)
	}
	if n := s.poweredOnCount(); n != 0 {
		t.Fatalf("a suspended lease does not count toward the limit, got %d", n)
	}

	if id := lease(t, "sess-b", PlatformIOS); id != "UDID-2" {
		t.Fatalf("the freed slot powers on another device, got %q", id)
	}
	if err := leaseErr("sess-a", PlatformIOS); !errors.Is(err, ErrExhausted) {
		t.Fatalf("resuming obeys the limit, got %v", err)
	}
	pause(t, "sess-b")
	if id := lease(t, "sess-a", PlatformIOS); id != "UDID-1" {
		t.Fatalf("the next lease of the session gets the same device back, got %q", id)
	}
	if !f.isOn("UDID-1") {
		t.Fatal("the resumed device is powered on")
	}
}

func TestLeaseResumesSameDeviceWithoutWipe(t *testing.T) {
	f := useFake(t)
	seed(t, bank(iosDevice(1), androidDevice(1)))
	if err := SetMaxOn(2); err != nil {
		t.Fatal(err)
	}
	ios := lease(t, "sess-a", PlatformIOS)
	android := lease(t, "sess-a", PlatformAndroid)

	pause(t, "sess-a")
	if f.isOn(ios) || f.isOn(android) {
		t.Fatal("pause powers off every virtual device of the session")
	}
	f.resetCalls()
	if lease(t, "sess-a", PlatformIOS) != ios || lease(t, "sess-a", PlatformAndroid) != android {
		t.Fatal("resume must return the same devices")
	}
	if f.count("eraseIOS") != 0 || f.count("startAVD atelier-android-1 wipe=true") != 0 {
		t.Fatalf("resume must not erase, got %v", f.calls)
	}
	if f.count("startAVD atelier-android-1 wipe=false") != 1 || f.count("bootIOS") != 1 {
		t.Fatalf("resume boots each device once, got %v", f.calls)
	}
}

func TestPauseIdempotentNoLeaseAndPhysical(t *testing.T) {
	f := useFake(t)
	f.phys = []PhysicalDevice{{ID: "serial-9", Name: "Pixel", Platform: PlatformAndroid}}
	seed(t, bank(iosDevice(1)))
	lease(t, "sess-a", PlatformIOS)
	pause(t, "sess-a")
	f.resetCalls()
	pause(t, "sess-a")
	if f.count("shutdownIOS") != 0 {
		t.Fatalf("a second pause is a no-op, got %v", f.calls)
	}

	before, err := os.ReadFile(paths.Devices())
	if err != nil {
		t.Fatal(err)
	}
	pause(t, "sess-nobody")
	after, err := os.ReadFile(paths.Devices())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("pause without a lease must leave the state untouched")
	}

	if id := lease(t, "sess-p", PlatformAndroid); id != "serial-9" {
		t.Fatalf("expected the physical fallback, got %q", id)
	}
	pause(t, "sess-p")
	if l := load(t).FindLease("sess-p", PlatformAndroid); l == nil || l.Suspended() {
		t.Fatalf("pause leaves a physical lease as it is, got %+v", l)
	}
}

func TestPausePowerOffFailureKeepsLeaseActive(t *testing.T) {
	f := useFake(t)
	seed(t, bank(iosDevice(1), androidDevice(1)))
	if err := SetMaxOn(2); err != nil {
		t.Fatal(err)
	}
	lease(t, "sess-a", PlatformIOS)
	lease(t, "sess-a", PlatformAndroid)
	f.fail["killAVD"] = 1

	err := Pause(context.Background(), "sess-a")
	if err == nil || !strings.Contains(err.Error(), "pause atelier-android-1") {
		t.Fatalf("a refused power-off is reported, got %v", err)
	}
	s := load(t)
	if l := s.FindLease("sess-a", PlatformIOS); !l.Suspended() || f.isOn("UDID-1") {
		t.Fatalf("the other lease is still paused, got %+v", l)
	}
	if l := s.FindLease("sess-a", PlatformAndroid); l.Suspended() || s.FindDevice("atelier-android-1").State != StateOn {
		t.Fatalf("the refused lease stays active and on, got %+v", l)
	}
	if n := s.poweredOnCount(); n != 1 {
		t.Fatalf("the device still on keeps counting, got %d", n)
	}
}
