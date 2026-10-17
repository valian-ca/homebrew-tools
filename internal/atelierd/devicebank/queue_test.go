package devicebank

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestWaitZeroEnqueuesAndExits10: with the slot taken, --wait 0
// exits 10 and leaves the caller visible in the queue, the holder with its
// power-on time.
func TestWaitZeroEnqueuesAndExits10(t *testing.T) {
	useFake(t)
	c := useClock(t, t0)
	seed(t, bank(androidDevice(1), androidDevice(2)))
	lease(t, "sess-1", PlatformAndroid)
	c.advance(time.Minute)

	if err := leaseErr("sess-2", PlatformAndroid); !errors.Is(err, ErrExhausted) {
		t.Fatalf("expected exit 10 (ErrExhausted), got %v", err)
	}
	rows, queue, err := StatusRows(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(queue) != 1 || queue[0].SessionID != "sess-2" || !queue[0].EnqueuedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("sess-2 must be queued, got %+v", queue)
	}
	held := rows[0]
	if held.Lease == nil || held.Lease.SessionID != "sess-1" || !held.Device.PoweredOnAt.Equal(t0) {
		t.Fatalf("status must show sess-1 holding the device on since t0, got %+v", held)
	}
}

func TestQueueKeepsPlaceAcrossCalls(t *testing.T) {
	useFake(t)
	c := useClock(t, t0)
	seed(t, bank(iosDevice(1), iosDevice(2)))
	lease(t, "sess-1", PlatformIOS)
	_ = leaseErr("sess-2", PlatformIOS)
	c.advance(time.Minute)
	_ = leaseErr("sess-3", PlatformIOS)
	c.advance(time.Minute)
	_ = leaseErr("sess-2", PlatformIOS)

	q := load(t).orderedQueue()
	if len(q) != 2 || q[0].SessionID != "sess-2" || !q[0].EnqueuedAt.Equal(t0) {
		t.Fatalf("a repeated call keeps its arrival time, got %+v", q)
	}
}

// TestHeadServedBeforeNewcomer: when the holder suspends or
// releases, the head of the queue is served before a newcomer.
func TestHeadServedBeforeNewcomer(t *testing.T) {
	for _, free := range []struct {
		name string
		fn   func(t *testing.T)
	}{
		{"pause", func(t *testing.T) { pause(t, "sess-1") }},
		{"release", func(t *testing.T) {
			if err := Release(context.Background(), "sess-1", ""); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(free.name, func(t *testing.T) {
			useFake(t)
			seed(t, bank(iosDevice(1), iosDevice(2)))
			lease(t, "sess-1", PlatformIOS)
			if err := leaseErr("sess-2", PlatformIOS); !errors.Is(err, ErrExhausted) {
				t.Fatalf("sess-2 queues, got %v", err)
			}
			free.fn(t)

			if err := leaseErr("sess-3", PlatformIOS); !errors.Is(err, ErrExhausted) {
				t.Fatalf("a newcomer must not pass the head, got %v", err)
			}
			lease(t, "sess-2", PlatformIOS)
			q := load(t).orderedQueue()
			if len(q) != 1 || q[0].SessionID != "sess-3" {
				t.Fatalf("sess-3 waits behind, sess-2 left the queue, got %+v", q)
			}
		})
	}
}

func TestExpiredWaiterLosesPlace(t *testing.T) {
	useFake(t)
	c := useClock(t, t0)
	seed(t, bank(iosDevice(1), iosDevice(2)))
	lease(t, "sess-1", PlatformIOS)
	_ = leaseErr("sess-2", PlatformIOS)
	pause(t, "sess-1")

	c.advance(QueueGrace + time.Second)
	if id := lease(t, "sess-3", PlatformIOS); id == "" {
		t.Fatal("a waiter gone 2 min past its deadline no longer holds its place")
	}
	if findWaiter(load(t), "sess-2", PlatformIOS) != nil {
		t.Fatal("the expired waiter is dropped")
	}
}

func TestReleaseAndSessionEndRemoveWaiters(t *testing.T) {
	useFake(t)
	seed(t, bank(iosDevice(1)))
	lease(t, "sess-1", PlatformIOS)
	_ = leaseErr("sess-2", PlatformIOS)
	_ = leaseErr("sess-3", PlatformIOS)

	if err := Release(context.Background(), "sess-2", ""); err != nil {
		t.Fatal(err)
	}
	OnEmit("sess-3", true)
	if q := load(t).Queue; len(q) != 0 {
		t.Fatalf("release and session-end drop the session's waiters, got %+v", q)
	}
}

// TestCrossPlatformHeadDoesNotBlock: an Android head blocked by exhaustion
// does not hold back an iOS caller the free slot can serve.
func TestCrossPlatformHeadDoesNotBlock(t *testing.T) {
	useFake(t)
	seed(t, bank(androidDevice(1), iosDevice(1)))
	if err := SetMaxOn(2); err != nil {
		t.Fatal(err)
	}
	lease(t, "sess-1", PlatformAndroid)
	if err := leaseErr("sess-2", PlatformAndroid); !errors.Is(err, ErrExhausted) {
		t.Fatalf("no android device left, got %v", err)
	}
	if id := lease(t, "sess-3", PlatformIOS); id != "UDID-1" {
		t.Fatalf("the iOS caller is servable and must be served, got %q", id)
	}
}

func TestLimitBlockedNeverFallsBackToPhysical(t *testing.T) {
	f := useFake(t)
	f.phys = []PhysicalDevice{{ID: "serial-9", Name: "Pixel", Platform: PlatformAndroid}}
	seed(t, bank(iosDevice(1), androidDevice(1)))
	lease(t, "sess-1", PlatformIOS)

	if err := leaseErr("sess-2", PlatformAndroid); !errors.Is(err, ErrExhausted) {
		t.Fatalf("an off AVD exists: the caller waits for it, never takes the physical device, got %v", err)
	}

	f2 := useFake(t)
	f2.phys = f.phys
	seed(t, bank(iosDevice(1)))
	if id := lease(t, "sess-2", PlatformAndroid); id != "serial-9" {
		t.Fatalf("without any android virtual device the physical one is leased, got %q", id)
	}
}

func TestLeaseWaitsForItsTurn(t *testing.T) {
	useFake(t)
	seed(t, bank(iosDevice(1)))
	lease(t, "sess-1", PlatformIOS)

	got := make(chan error, 1)
	go func() {
		_, err := Acquire(context.Background(), "sess-2", "/wd", PlatformIOS, 5*time.Second, discard{})
		got <- err
	}()
	time.Sleep(50 * time.Millisecond)
	if err := Release(context.Background(), "sess-1", ""); err != nil {
		t.Fatal(err)
	}
	if err := <-got; err != nil {
		t.Fatalf("a waiting lease is served once the device is recycled, got %v", err)
	}
}

// TestWaiterNotifiedOnceAfterTenMinutes: one notification per waiter.
func TestWaiterNotifiedOnceAfterTenMinutes(t *testing.T) {
	f := useFake(t)
	c := useClock(t, t0)
	seed(t, bank(iosDevice(1)))
	lease(t, "sess-1", PlatformIOS)
	for range 13 {
		_ = leaseErr("sess-2", PlatformIOS)
		c.advance(time.Minute)
	}
	if len(f.notes) != 1 {
		t.Fatalf("exactly one notification after 10 min of waiting, got %v", f.notes)
	}
}

func TestHolderWarning(t *testing.T) {
	useFake(t)
	seed(t, bank(iosDevice(1)))
	lease(t, "sess-1", PlatformIOS)
	if msg := HolderWarning("sess-1"); msg != "" {
		t.Fatalf("no waiter, no warning, got %q", msg)
	}
	_ = leaseErr("sess-2", PlatformIOS)
	if msg := HolderWarning("sess-1"); msg == "" {
		t.Fatal("the holder is warned once someone waits")
	}
	if msg := HolderWarning("sess-2"); msg != "" {
		t.Fatalf("the waiter holds nothing, got %q", msg)
	}
	pause(t, "sess-1")
	if msg := HolderWarning("sess-1"); msg != "" {
		t.Fatalf("a suspended holder is not warned, got %q", msg)
	}
}
