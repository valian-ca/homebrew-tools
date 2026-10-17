package devicebank

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestColdRecycleNeverBoots(t *testing.T) {
	t.Run("ios", func(t *testing.T) {
		f := useFake(t)
		seed(t, bank(iosDevice(1)))
		lease(t, "sess-a", PlatformIOS)
		f.resetCalls()

		if err := Release(context.Background(), "sess-a", ""); err != nil {
			t.Fatal(err)
		}
		if n := f.count("bootIOS"); n != 0 {
			t.Fatalf("recycle must never boot, got %d boot calls: %v", n, f.calls)
		}
		if f.count("shutdownIOS") != 1 || f.count("eraseIOS") != 1 {
			t.Fatalf("recycle must power off then erase, got %v", f.calls)
		}
		d := load(t).FindDevice("atelier-ios-1")
		if d.State != StateOff || f.isOn("UDID-1") {
			t.Fatalf("recycled device must be off, state %s, really on %v", d.State, f.isOn("UDID-1"))
		}
	})

	t.Run("android erases by wiping at the next boot", func(t *testing.T) {
		f := useFake(t)
		seed(t, bank(androidDevice(1)))
		serial := lease(t, "sess-a", PlatformAndroid)
		f.resetCalls()

		if err := Release(context.Background(), "sess-a", ""); err != nil {
			t.Fatal(err)
		}
		if n := f.count("startAVD"); n != 0 {
			t.Fatalf("recycle must never start the emulator, got %v", f.calls)
		}
		d := load(t).FindDevice("atelier-android-1")
		if d.State != StateOff || !d.WipePending || f.isOn(serial) {
			t.Fatalf("recycled AVD must be off with a wipe pending, got %+v (on %v)", d, f.isOn(serial))
		}

		f.resetCalls()
		lease(t, "sess-b", PlatformAndroid)
		if f.count("startAVD atelier-android-1 wipe=true") != 1 {
			t.Fatalf("the next boot after a recycle must wipe, got %v", f.calls)
		}
		if load(t).FindDevice("atelier-android-1").WipePending {
			t.Fatal("a successful wipe boot clears the pending wipe")
		}
	})
}

func TestRecycleTwoFailuresBreaks(t *testing.T) {
	f := useFake(t)
	seed(t, bank(iosDevice(1)))
	lease(t, "sess-a", PlatformIOS)
	f.fail["eraseIOS"] = 2

	if err := Release(context.Background(), "sess-a", ""); err != nil {
		t.Fatal(err)
	}
	if len(f.spawned) != 2 {
		t.Fatalf("one worker plus exactly one re-spawn, got %v", f.spawned)
	}
	d := load(t).FindDevice("atelier-ios-1")
	if d.State != StateBroken || !strings.Contains(d.BrokenReason, "eraseIOS") {
		t.Fatalf("second failure must leave the device broken with its reason, got %+v", d)
	}
	if err := leaseErr("sess-b", PlatformIOS); !errors.Is(err, ErrExhausted) {
		t.Fatalf("a broken device is never leased, got %v", err)
	}
}

func TestStuckWorkerCountsAsFailure(t *testing.T) {
	f := useFake(t)
	f.runSpawns = false
	c := useClock(t, t0)
	d := iosDevice(1)
	d.State = StateRecycling
	d.RecycleStartedAt = t0
	seed(t, bank(d))

	c.advance(RecycleBound + time.Minute)
	if err := Reap(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := load(t).FindDevice("atelier-ios-1")
	if got.State != StateRecycling || got.RecycleFailures != 1 || len(f.spawned) != 1 {
		t.Fatalf("a silent worker counts one failure and is re-spawned once, got %+v spawned %v", got, f.spawned)
	}

	c.advance(RecycleBound + time.Minute)
	if err := Reap(context.Background()); err != nil {
		t.Fatal(err)
	}
	got = load(t).FindDevice("atelier-ios-1")
	if got.State != StateBroken || got.BrokenReason != "recycle worker did not finish within 5m" {
		t.Fatalf("a second silent worker breaks the device, got %+v", got)
	}

	c.advance(RecycleBound)
	if err := Reap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.spawned) != 1 {
		t.Fatalf("a broken device is never re-spawned, got %v", f.spawned)
	}
}

func TestRecycleWithinBoundIsLeftAlone(t *testing.T) {
	f := useFake(t)
	f.runSpawns = false
	c := useClock(t, t0)
	d := iosDevice(1)
	d.State = StateRecycling
	d.RecycleStartedAt = t0
	seed(t, bank(d))

	c.advance(RecycleBound)
	if err := Reap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := load(t).FindDevice("atelier-ios-1"); got.RecycleFailures != 0 || len(f.spawned) != 0 {
		t.Fatalf("a recycle within its bound is not stuck, got %+v", got)
	}
}

func TestBootFailuresCountTowardBroken(t *testing.T) {
	f := useFake(t)
	seed(t, bank(iosDevice(1)))

	f.fail["bootIOS"] = 1
	if id := lease(t, "sess-a", PlatformIOS); id != "UDID-1" {
		t.Fatalf("after one failed boot and its recycle the device is retried, got %q", id)
	}
	if n := load(t).FindDevice("atelier-ios-1").RecycleFailures; n != 0 {
		t.Fatalf("a successful boot resets the counter, got %d", n)
	}

	if err := Release(context.Background(), "sess-a", ""); err != nil {
		t.Fatal(err)
	}
	f.fail["bootIOS"] = 2
	if err := leaseErr("sess-b", PlatformIOS); !errors.Is(err, ErrExhausted) {
		t.Fatalf("two failed boots in a row break the only device, got %v", err)
	}
	if d := load(t).FindDevice("atelier-ios-1"); d.State != StateBroken || !strings.Contains(d.BrokenReason, "bootIOS") {
		t.Fatalf("expected broken with the boot error, got %+v", d)
	}
}

func TestBankInitRepairsBroken(t *testing.T) {
	useFake(t)
	d := iosDevice(1)
	d.State = StateBroken
	d.BrokenReason = "simctl erase: exit 1"
	d.RecycleFailures = 2
	s := bank(d)
	repair(s, d, io.Discard)
	if d.State != StateOff || d.RecycleFailures != 0 || d.BrokenReason != "" {
		t.Fatalf("repair must reset a broken device, got %+v", d)
	}

	if err := SetMaxOn(3); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")
	t.Setenv("ANDROID_HOME", t.TempDir())
	if err := InitBank(context.Background(), 0, 0, io.Discard); err != nil {
		t.Fatal(err)
	}
	if n, err := MaxOn(); err != nil || n != 3 {
		t.Fatalf("bank init must keep the ignition limit, got %d (%v)", n, err)
	}
}
