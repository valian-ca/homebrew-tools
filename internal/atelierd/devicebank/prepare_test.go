package devicebank

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func prepare(session string, platform Platform, prep Preparation) error {
	return Prepare(context.Background(), session, platform, prep)
}

func TestPrepareActsOnLeasedDeviceAndAppOnly(t *testing.T) {
	f := useFake(t)
	f.plug(pixel)
	seed(t, bank(androidDevice(1)))
	leaseAny(t, "sess-1", PlatformAndroid)
	f.resetCalls()

	err := prepare("sess-1", PlatformAndroid, Preparation{
		Reverses: []string{"tcp:8080", "tcp:9099"}, Clear: true, Grants: []string{"android.permission.CAMERA"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"clearApp " + pixel.ID + " " + runApp,
		"grantPermission " + pixel.ID + " " + runApp + " android.permission.CAMERA",
		"addReverse " + pixel.ID + " tcp:8080",
		"addReverse " + pixel.ID + " tcp:9099",
	}
	if !slices.Equal(f.calls, want) {
		t.Fatalf("clear, then grants, then reverses, on the leased Pixel and the lease's app id only: got %v", f.calls)
	}
	release(t, "sess-1")
	if f.reverses[pixel.Serial] != 0 {
		t.Fatalf("the return removes the reverses, got %d", f.reverses[pixel.Serial])
	}
}

func TestPrepareRefusesWithoutHeldPhysicalAndroidLease(t *testing.T) {
	f := useFake(t)
	f.plug(pixel, iphone)
	seed(t, bank(iosDevice(1), androidDevice(1)))
	if err := SetMaxOn(2); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name     string
		setup    func()
		platform Platform
		prep     Preparation
		want     string
	}{
		{"no lease", func() {}, PlatformAndroid, Preparation{Clear: true}, "holds no android lease"},
		{"ios", func() { leaseAny(t, "sess-1", PlatformIOS) }, PlatformIOS, Preparation{Clear: true}, "physical Android device only"},
		{"virtual lease", func() { lease(t, "sess-2", PlatformAndroid) }, PlatformAndroid, Preparation{Clear: true}, "a virtual device"},
		{"suspended", func() { leaseAny(t, "sess-3", PlatformAndroid); pause(t, "sess-3") }, PlatformAndroid, Preparation{Clear: true}, "is suspended"},
		{"bad reverse", func() {}, PlatformAndroid, Preparation{Reverses: []string{"tcp:80 shell rm"}}, "invalid --reverse"},
		{"bad grant", func() {}, PlatformAndroid, Preparation{Grants: []string{"x; rm -rf /"}}, "invalid --grant"},
	} {
		tt.setup()
		f.resetCalls()
		session := map[string]string{"virtual lease": "sess-2", "suspended": "sess-3"}[tt.name]
		if session == "" {
			session = "sess-1"
		}
		err := prepare(session, tt.platform, tt.prep)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Fatalf("%s: want an error containing %q, got %v", tt.name, tt.want, err)
		}
		for _, op := range []string{"clearApp", "grantPermission", "addReverse"} {
			if f.count(op) != 0 {
				t.Fatalf("%s: nothing is done on any device, got %v", tt.name, f.calls)
			}
		}
	}
}

func TestPausePlatformSuspendsOnlyThatPlatform(t *testing.T) {
	f := useFake(t)
	seed(t, bank(iosDevice(1), androidDevice(1)))
	if err := SetMaxOn(2); err != nil {
		t.Fatal(err)
	}
	lease(t, "sess-1", PlatformIOS)
	lease(t, "sess-1", PlatformAndroid)

	if err := Pause(context.Background(), "sess-1", PlatformIOS); err != nil {
		t.Fatal(err)
	}
	s := load(t)
	if !s.FindLease("sess-1", PlatformIOS).Suspended() || s.FindLease("sess-1", PlatformAndroid).Suspended() {
		t.Fatalf("only the iOS lease is suspended, got %+v", s.Leases)
	}
	if f.isOn("UDID-1") || !f.isOn(emulator(1)) {
		t.Fatal("the simulator is off, the emulator stays on")
	}
}
