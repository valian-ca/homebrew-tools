package devicebank

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestDeviceToPowerOnSkipsHeldBrokenAndRecycling(t *testing.T) {
	s := bank(iosDevice(1), iosDevice(2), iosDevice(3), iosDevice(4))
	commitLease(s, &Candidate{Device: s.Devices[0]}, "sess-a", "/wd", PlatformIOS, t0)
	s.Leases[0].SuspendedAt = t0
	s.Devices[0].State = StateOff
	s.Devices[1].State = StateBroken
	s.Devices[2].State = StateRecycling
	if d := deviceToPowerOn(s, "sess-b", PlatformIOS, nil); d == nil || d.Name != "atelier-ios-4" {
		t.Fatalf("only the unleased off device is a candidate, got %+v", d)
	}
	if d := deviceToPowerOn(s, "sess-a", PlatformIOS, nil); d == nil || d.Name != "atelier-ios-1" {
		t.Fatalf("a suspended lease reserves its own device, got %+v", d)
	}
}

func TestReleaseSessionRecyclesVirtualOnly(t *testing.T) {
	s := bank(iosDevice(1), iosDevice(2))
	commitLease(s, &Candidate{Device: s.Devices[0]}, "sess-a", "/wd", PlatformIOS, t0)
	commitLease(s, &Candidate{Physical: &PhysicalDevice{ID: "serial-1", Name: "Pixel", Platform: PlatformAndroid}}, "sess-a", "/wd", PlatformAndroid, t0)

	releaseSession(s, "sess-a", "", t0.Add(time.Minute), "release")
	if len(s.spawns) != 1 || s.spawns[0] != "atelier-ios-1" {
		t.Fatalf("only the virtual device recycles, got %v", s.spawns)
	}
	if s.Devices[0].State != StateRecycling || s.Devices[0].PowerOffConfirmed {
		t.Fatalf("a released on device is recycling, not yet confirmed off, got %+v", s.Devices[0])
	}
	if len(s.Leases) != 0 {
		t.Fatalf("all session leases must be gone, got %d", len(s.Leases))
	}
}

func TestReleaseSessionFiltersByPlatform(t *testing.T) {
	s := bank(iosDevice(1))
	commitLease(s, &Candidate{Device: s.Devices[0]}, "sess-a", "/wd", PlatformIOS, t0)
	commitLease(s, &Candidate{Physical: &PhysicalDevice{ID: "serial-1", Name: "Pixel", Platform: PlatformAndroid}}, "sess-a", "/wd", PlatformAndroid, t0)

	releaseSession(s, "sess-a", PlatformAndroid, t0, "release")
	if s.FindLease("sess-a", PlatformIOS) == nil {
		t.Fatal("the iOS lease must survive an android-only release")
	}
	if s.FindLease("sess-a", PlatformAndroid) != nil {
		t.Fatal("the android lease must be gone")
	}
}

func TestReapExpiredFreesTTLLeases(t *testing.T) {
	s := bank(iosDevice(1), iosDevice(2))
	commitLease(s, &Candidate{Device: s.Devices[0]}, "sess-old", "/wd", PlatformIOS, t0)
	commitLease(s, &Candidate{Device: s.Devices[1]}, "sess-fresh", "/wd", PlatformIOS, t0)
	s.FindLease("sess-fresh", PlatformIOS).RenewedAt = t0.Add(TTL)

	if !reapExpired(s, t0.Add(TTL+time.Minute)) {
		t.Fatal("the expired lease must be reaped")
	}
	if s.FindLease("sess-old", PlatformIOS) != nil || s.Devices[0].State != StateRecycling {
		t.Fatal("expired lease must be dropped and its device recycled")
	}
	if s.FindLease("sess-fresh", PlatformIOS) == nil {
		t.Fatal("renewed lease must survive")
	}
}

func TestTouchSessionRenewsAllLeases(t *testing.T) {
	s := bank(iosDevice(1), iosDevice(2))
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

func TestConcurrentLeaseUnderFlock(t *testing.T) {
	useFake(t)
	seed(t, &State{Config: Config{IOS: 2, MaxOn: 2}, Devices: []*Device{iosDevice(1), iosDevice(2)}})

	ids := make([]string, 2)
	var wg sync.WaitGroup
	for i, session := range []string{"sess-a", "sess-b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := Acquire(context.Background(), session, "/wd", PlatformIOS, 0, discard{})
			if err != nil {
				t.Error(err)
			}
			ids[i] = id
		}()
	}
	wg.Wait()

	if ids[0] == "" || ids[1] == "" || ids[0] == ids[1] {
		t.Fatalf("concurrent sessions must get distinct devices, got %q and %q", ids[0], ids[1])
	}
	if n := len(load(t).Leases); n != 2 {
		t.Fatalf("status must show two distinct leases, got %d", n)
	}
}
