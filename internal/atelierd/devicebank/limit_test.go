package devicebank

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestConcurrentSessionsNeverExceedLimit(t *testing.T) {
	t.Run("two sessions racing", func(t *testing.T) {
		f := useFake(t)
		seed(t, bank(iosDevice(1), iosDevice(2), androidDevice(1)))

		var wg sync.WaitGroup
		for _, sp := range []struct {
			session  string
			platform Platform
		}{{"sess-1", PlatformIOS}, {"sess-2", PlatformAndroid}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range 4 {
					if _, err := Acquire(context.Background(), sp.session, "/wd", sp.platform, 10*time.Second, discard{}); err != nil {
						t.Error(err)
						return
					}
					time.Sleep(10 * time.Millisecond)
					if err := Release(context.Background(), sp.session, ""); err != nil {
						t.Error(err)
						return
					}
				}
			}()
		}
		wg.Wait()
		if f.maxOnSeen != 1 {
			t.Fatalf("at most one virtual device on at once, saw %d", f.maxOnSeen)
		}
	})

	t.Run("a recycling device counts until its power-off", func(t *testing.T) {
		f := useFake(t)
		f.runSpawns = false
		seed(t, bank(iosDevice(1), iosDevice(2)))
		lease(t, "sess-1", PlatformIOS)
		if err := Release(context.Background(), "sess-1", ""); err != nil {
			t.Fatal(err)
		}
		if err := leaseErr("sess-2", PlatformIOS); !errors.Is(err, ErrExhausted) {
			t.Fatalf("the recycling device is still on: the limit must hold, got %v", err)
		}
		if err := RunRecycle(context.Background(), "atelier-ios-1"); err != nil {
			t.Fatal(err)
		}
		lease(t, "sess-2", PlatformIOS)
		if f.maxOnSeen != 1 {
			t.Fatalf("at most one virtual device on at once, saw %d", f.maxOnSeen)
		}
	})
}

func TestLimitTwoAllowsTwo(t *testing.T) {
	f := useFake(t)
	seed(t, bank(iosDevice(1), iosDevice(2), iosDevice(3)))
	if err := SetMaxOn(2); err != nil {
		t.Fatal(err)
	}
	lease(t, "sess-1", PlatformIOS)
	lease(t, "sess-2", PlatformIOS)
	if err := leaseErr("sess-3", PlatformIOS); !errors.Is(err, ErrExhausted) {
		t.Fatalf("a third device exceeds a limit of two, got %v", err)
	}
	if f.maxOnSeen != 2 {
		t.Fatalf("limit 2 allows two devices on, saw %d", f.maxOnSeen)
	}
	if err := SetMaxOn(0); err == nil {
		t.Fatal("a limit below one is refused")
	}
}
