package devicebank

import (
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)

func bankOfTwo() *State {
	return &State{
		Config: Config{IOS: 2},
		Devices: []*Device{
			{Name: "atelier-ios-1", Platform: PlatformIOS, UDID: "UDID-1", State: StateFree, LastUsedAt: t0},
			{Name: "atelier-ios-2", Platform: PlatformIOS, UDID: "UDID-2", State: StateFree, LastUsedAt: t0},
		},
	}
}

func TestAcquireExistingIsIdempotent(t *testing.T) {
	s := bankOfTwo()
	first := commitLease(s, pickCandidate(s, PlatformIOS, nil), "sess-a", "/wd", PlatformIOS, t0)

	again := acquireExisting(s, "sess-a", PlatformIOS, t0.Add(time.Minute))
	if again == nil || again.DeviceID != first.DeviceID {
		t.Fatalf("same session must get the same device back, got %+v want %s", again, first.DeviceID)
	}
	if !again.RenewedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("idempotent acquire must renew the lease, RenewedAt=%v", again.RenewedAt)
	}
	if len(s.Leases) != 1 {
		t.Fatalf("idempotent acquire must not add a lease, got %d", len(s.Leases))
	}
}

func TestPickCandidatePrefersBootedFreeOverOff(t *testing.T) {
	s := bankOfTwo()
	s.Devices[0].State = StateOff
	c := pickCandidate(s, PlatformIOS, nil)
	if c == nil || c.Device.Name != "atelier-ios-2" {
		t.Fatalf("expected the booted free device first, got %+v", c)
	}
}

func TestPickCandidateFallsBackToPhysical(t *testing.T) {
	s := bankOfTwo()
	s.Devices[0].State = StateLeased
	s.Devices[1].State = StateRecycling
	phys := []PhysicalDevice{{ID: "serial-1", Name: "Pixel", Platform: PlatformAndroid}, {ID: "00008110", Name: "iPhone", Platform: PlatformIOS}}
	c := pickCandidate(s, PlatformIOS, phys)
	if c == nil || c.Physical == nil || c.Physical.ID != "00008110" {
		t.Fatalf("expected the iOS physical device, got %+v", c)
	}
}

func TestPickCandidateExhausted(t *testing.T) {
	s := bankOfTwo()
	s.Devices[0].State = StateLeased
	s.Devices[1].State = StateRecycling
	if c := pickCandidate(s, PlatformIOS, nil); c != nil {
		t.Fatalf("leased + recycling bank must be exhausted, got %+v", c)
	}
}

func TestReleaseSessionRecyclesVirtualOnly(t *testing.T) {
	s := bankOfTwo()
	commitLease(s, &Candidate{Device: s.Devices[0]}, "sess-a", "/wd", PlatformIOS, t0)
	commitLease(s, &Candidate{Physical: &PhysicalDevice{ID: "serial-1", Name: "Pixel", Platform: PlatformAndroid}}, "sess-a", "/wd", PlatformAndroid, t0)

	recycled := releaseSession(s, "sess-a", "", t0.Add(time.Minute))
	if len(recycled) != 1 || recycled[0].Name != "atelier-ios-1" {
		t.Fatalf("only the virtual device recycles, got %+v", recycled)
	}
	if recycled[0].State != StateRecycling {
		t.Fatalf("released virtual device must be recycling, got %s", recycled[0].State)
	}
	if len(s.Leases) != 0 {
		t.Fatalf("all session leases must be gone, got %d", len(s.Leases))
	}
}

func TestReleaseSessionFiltersByPlatform(t *testing.T) {
	s := bankOfTwo()
	commitLease(s, &Candidate{Device: s.Devices[0]}, "sess-a", "/wd", PlatformIOS, t0)
	commitLease(s, &Candidate{Physical: &PhysicalDevice{ID: "serial-1", Name: "Pixel", Platform: PlatformAndroid}}, "sess-a", "/wd", PlatformAndroid, t0)

	releaseSession(s, "sess-a", PlatformAndroid, t0)
	if s.FindLease("sess-a", PlatformIOS) == nil {
		t.Fatal("the iOS lease must survive an android-only release")
	}
	if s.FindLease("sess-a", PlatformAndroid) != nil {
		t.Fatal("the android lease must be gone")
	}
}

func TestReapExpiredFreesTTLLeases(t *testing.T) {
	s := bankOfTwo()
	commitLease(s, &Candidate{Device: s.Devices[0]}, "sess-old", "/wd", PlatformIOS, t0)
	commitLease(s, &Candidate{Device: s.Devices[1]}, "sess-fresh", "/wd", PlatformIOS, t0)
	s.FindLease("sess-fresh", PlatformIOS).RenewedAt = t0.Add(TTL)

	recycled := reapExpired(s, t0.Add(TTL+time.Minute))
	if len(recycled) != 1 || recycled[0].Name != "atelier-ios-1" {
		t.Fatalf("only the expired lease's device recycles, got %+v", recycled)
	}
	if s.FindLease("sess-old", PlatformIOS) != nil {
		t.Fatal("expired lease must be reaped")
	}
	if s.FindLease("sess-fresh", PlatformIOS) == nil {
		t.Fatal("renewed lease must survive")
	}
	// AC 8: the freed device is acquirable by another session once recycled.
	recycled[0].State = StateFree
	if c := pickCandidate(s, PlatformIOS, nil); c == nil || c.Device.Name != "atelier-ios-1" {
		t.Fatalf("reaped device must be leasable again, got %+v", c)
	}
}

func TestTouchSessionRenewsAllLeases(t *testing.T) {
	s := bankOfTwo()
	commitLease(s, &Candidate{Device: s.Devices[0]}, "sess-a", "/wd", PlatformIOS, t0)
	commitLease(s, &Candidate{Physical: &PhysicalDevice{ID: "serial-1", Name: "Pixel", Platform: PlatformAndroid}}, "sess-a", "/wd", PlatformAndroid, t0)
	commitLease(s, &Candidate{Device: s.Devices[1]}, "sess-b", "/wd", PlatformIOS, t0)

	later := t0.Add(10 * time.Minute)
	if n := touchSession(s, "sess-a", later); n != 2 {
		t.Fatalf("expected 2 leases touched, got %d", n)
	}
	if !s.FindLease("sess-a", PlatformIOS).RenewedAt.Equal(later) {
		t.Fatal("sess-a iOS lease must be renewed")
	}
	if s.FindLease("sess-b", PlatformIOS).RenewedAt.Equal(later) {
		t.Fatal("sess-b lease must not be renewed by sess-a traffic")
	}
}

func TestIdleDevicesAndStuckRecycles(t *testing.T) {
	s := bankOfTwo()
	s.Devices[1].State = StateRecycling
	s.Devices[1].RecycleStartedAt = t0

	now := t0.Add(IdleShutdown + time.Minute)
	idle := idleDevices(s, now)
	if len(idle) != 1 || idle[0].Name != "atelier-ios-1" {
		t.Fatalf("expected the free idle device, got %+v", idle)
	}
	stuck := stuckRecycles(s, now)
	if len(stuck) != 1 || stuck[0].Name != "atelier-ios-2" {
		t.Fatalf("expected the stuck recycling device, got %+v", stuck)
	}
	if got := stuckRecycles(s, t0.Add(StuckRecycle)); got != nil {
		t.Fatalf("a recycle within the window is not stuck, got %+v", got)
	}
}

// TestConcurrentLeaseUnderFlock is AC 3: two sessions racing through the
// locked acquire transaction get two distinct devices and both leases land
// in the persisted state.
func TestConcurrentLeaseUnderFlock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedErr := WithLock(func(s *State) error {
		*s = *bankOfTwo()
		return nil
	})
	if seedErr != nil {
		t.Fatal(seedErr)
	}

	ids := make([]string, 2)
	var wg sync.WaitGroup
	for i, session := range []string{"sess-a", "sess-b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := WithLock(func(s *State) error {
				c := pickCandidate(s, PlatformIOS, nil)
				if c == nil {
					t.Error("bank exhausted during concurrent acquire")
					return errNoChange
				}
				ids[i] = commitLease(s, c, session, "/wd", PlatformIOS, time.Now()).DeviceID
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	if ids[0] == "" || ids[1] == "" || ids[0] == ids[1] {
		t.Fatalf("concurrent sessions must get distinct devices, got %q and %q", ids[0], ids[1])
	}
	final, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(final.Leases) != 2 {
		t.Fatalf("status must show two distinct leases, got %d", len(final.Leases))
	}
}
