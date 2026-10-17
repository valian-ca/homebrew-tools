package devicebank

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/paths"
)

func reap(t *testing.T) {
	t.Helper()
	if err := Reap(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestReapCorrectsDeadDevice: a leased device stopped by
// hand is off at the next reap, and its lease says the device died.
func TestReapCorrectsDeadDevice(t *testing.T) {
	f := useFake(t)
	seed(t, bank(iosDevice(1)))
	lease(t, "sess-1", PlatformIOS)
	f.setOn("UDID-1", false)

	reap(t)
	s := load(t)
	if d := s.FindDevice("atelier-ios-1"); d.State != StateOff {
		t.Fatalf("a device stopped by hand is off after reap, got %s", d.State)
	}
	if l := s.FindLease("sess-1", PlatformIOS); l == nil || !l.DeviceDied {
		t.Fatalf("the lease survives, marked device died, got %+v", l)
	}
}

// TestLeaseRestartsDeadDevice: the next lease of the
// session restarts its own device instead of handing back a dead one.
func TestLeaseRestartsDeadDevice(t *testing.T) {
	f := useFake(t)
	seed(t, bank(iosDevice(1), iosDevice(2)))
	lease(t, "sess-1", PlatformIOS)
	f.setOn("UDID-1", false)
	reap(t)

	lease(t, "sess-2", PlatformIOS)
	if err := leaseErr("sess-1", PlatformIOS); !errors.Is(err, ErrExhausted) {
		t.Fatalf("the restart goes through the limit, got %v", err)
	}
	pause(t, "sess-2")
	f.resetCalls()
	if id := lease(t, "sess-1", PlatformIOS); id != "UDID-1" {
		t.Fatalf("the session gets its own device back, got %q", id)
	}
	if f.count("bootIOS UDID-1") != 1 || !f.isOn("UDID-1") {
		t.Fatalf("the dead device is booted again, got %v", f.calls)
	}
	if l := load(t).FindLease("sess-1", PlatformIOS); l.DeviceDied {
		t.Fatal("a restarted device is no longer dead")
	}
}

func TestReapPowersOffUnleasedOn(t *testing.T) {
	f := useFake(t)
	on := iosDevice(1)
	on.State = StateOn
	seed(t, bank(on, iosDevice(2), androidDevice(1)))
	f.setOn("UDID-1", true)
	f.setOn("UDID-2", true)
	f.setOn("emulator-5584", true)

	reap(t)
	s := load(t)
	for _, d := range s.Devices {
		if d.State != StateOff || f.isOn(d.TargetID()) {
			t.Fatalf("an unleased device found on is powered off, got %s state %s on %v", d.Name, d.State, f.isOn(d.TargetID()))
		}
	}
}

func TestReapBootGraceAndProbeFailure(t *testing.T) {
	f := useFake(t)
	booting := iosDevice(1)
	booting.State = StateOn
	booting.Booting = true
	booting.PoweredOnAt = time.Now()
	s := bank(booting)
	s.Leases = []*Lease{{SessionID: "sess-1", Platform: PlatformIOS, DeviceName: "atelier-ios-1", DeviceID: "UDID-1", AcquiredAt: time.Now(), RenewedAt: time.Now()}}
	seed(t, s)

	reap(t)
	if d := load(t).FindDevice("atelier-ios-1"); d.State != StateOn {
		t.Fatalf("a device still booting is not declared dead, got %s", d.State)
	}

	f.probeErr = errors.New("simctl unavailable")
	if err := WithLock(func(s *State) error {
		s.FindDevice("atelier-ios-1").Booting = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	reap(t)
	if d := load(t).FindDevice("atelier-ios-1"); d.State != StateOn {
		t.Fatalf("a failed probe never flips a state, got %s", d.State)
	}
}

// TestLegacyFreeMigrates: a 0.20 state file loads with its leases intact;
// its booted free devices are powered off by the next reap.
func TestLegacyFreeMigrates(t *testing.T) {
	f := useFake(t)
	legacy := `{
  "config": {"ios": 2, "android": 0},
  "devices": [
    {"name": "atelier-ios-1", "platform": "ios", "udid": "UDID-1", "state": "free", "lastUsedAt": "2026-10-16T10:00:00Z"},
    {"name": "atelier-ios-2", "platform": "ios", "udid": "UDID-2", "state": "leased", "lastUsedAt": "2026-10-16T10:00:00Z"}
  ],
  "leases": [
    {"sessionId": "sess-1", "platform": "ios", "deviceName": "atelier-ios-2", "deviceId": "UDID-2", "physical": false, "workdir": "/wd", "acquiredAt": "2026-10-16T10:00:00Z", "renewedAt": "` + time.Now().UTC().Format(time.RFC3339) + `"}
  ]
}`
	if err := paths.EnsureDir(paths.MustRoot()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Devices(), []byte(legacy), paths.FileMode); err != nil {
		t.Fatal(err)
	}
	f.setOn("UDID-1", true)
	f.setOn("UDID-2", true)

	reap(t)
	s := load(t)
	if d := s.FindDevice("atelier-ios-1"); d.State != StateOff || f.isOn("UDID-1") {
		t.Fatalf("a legacy free device is powered off, got %s", d.State)
	}
	if d := s.FindDevice("atelier-ios-2"); d.State != StateOn {
		t.Fatalf("a legacy leased device is on, got %s", d.State)
	}
	if l := s.FindLease("sess-1", PlatformIOS); l == nil || l.DeviceID != "UDID-2" {
		t.Fatalf("the in-flight lease survives the upgrade, got %+v", l)
	}
	if id := lease(t, "sess-1", PlatformIOS); id != "UDID-2" {
		t.Fatalf("the upgraded lease stays idempotent, got %q", id)
	}
}

func TestSuspendedLeaseReapedByTTL(t *testing.T) {
	f := useFake(t)
	f.runSpawns = false
	c := useClock(t, t0)
	seed(t, bank(iosDevice(1)))
	lease(t, "sess-1", PlatformIOS)
	pause(t, "sess-1")

	c.advance(TTL + time.Minute)
	reap(t)
	s := load(t)
	d := s.FindDevice("atelier-ios-1")
	if s.FindLease("sess-1", PlatformIOS) != nil || d.State != StateRecycling || !d.RecycleOff || len(f.spawned) != 1 {
		t.Fatalf("a suspended lease past its TTL is recycled, got %+v spawned %v", d, f.spawned)
	}
	if n := s.poweredOnCount(); n != 0 {
		t.Fatalf("recycling an already-off device takes no slot, got %d", n)
	}
}
