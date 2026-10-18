package devicebank

import (
	"testing"
	"time"
)

func TestLeaseAnnotationsComposition(t *testing.T) {
	useFake(t)
	c := useClock(t, t0)

	if got := LeaseAnnotations(); got != nil {
		t.Fatalf("no bank file must mean no annotations, got %v", got)
	}

	on := &Device{Name: "atelier-ios-2", Platform: PlatformIOS, UDID: "BBB-2222", State: StateOn, PoweredOnAt: t0, LastUsedAt: t0}
	seed(t, &State{
		Config: Config{IOS: 3},
		Devices: []*Device{
			{Name: "atelier-ios-1", Platform: PlatformIOS, UDID: "AAA-1111", State: StateOff, LastUsedAt: t0},
			on,
			{Name: "atelier-ios-3", Platform: PlatformIOS, UDID: "CCC-3333", State: StateOff, LastUsedAt: t0},
			{Name: "atelier-android-1", Platform: PlatformAndroid, AVD: "atelier-android-1", Port: 5642, State: StateRecycling, LastUsedAt: t0},
		},
		Leases: []*Lease{
			{SessionID: "abc123", Platform: PlatformIOS, DeviceName: "atelier-ios-2", DeviceID: "BBB-2222", AcquiredAt: t0, RenewedAt: t0},
			{SessionID: "ghi789", Platform: PlatformIOS, DeviceName: "atelier-ios-3", DeviceID: "CCC-3333", AcquiredAt: t0, RenewedAt: t0, SuspendedAt: t0},
			{SessionID: "def456", Platform: PlatformAndroid, DeviceID: "serial-9", DeviceName: "Pixel", Physical: true, AcquiredAt: t0, RenewedAt: t0},
			{SessionID: "jkl000", Platform: PlatformIOS, DeviceID: "udid-phys", DeviceName: "My iPhone", Physical: true, AcquiredAt: t0, RenewedAt: t0, SuspendedAt: t0},
		},
		Queue: []*Waiter{{SessionID: "zzz999", Platform: PlatformIOS, EnqueuedAt: t0, ExpiresAt: t0.Add(time.Hour)}},
	})
	c.advance(12 * time.Minute)

	ann := LeaseAnnotations()
	tests := []struct {
		label, id, want string
	}{
		{" atelier-ios-1 (simulator)", "AAA-1111", " atelier-ios-1 (simulator) — bank, off"},
		{" atelier-ios-2 (simulator)", "BBB-2222", " atelier-ios-2 (simulator) — bank, on, session abc123 · zzz999 waiting 12 min"},
		{" atelier-ios-3 (simulator)", "CCC-3333", " atelier-ios-3 (simulator) — bank, suspended, session ghi789"},
		{" Android (emulator)", "emulator-5642", " Android (emulator) — bank, recycling"},
		{" Pixel 7 (usb)", "serial-9", " Pixel 7 (usb) — leased, session def456"},
		{" My iPhone (wifi)", "unrelated-udid", " My iPhone (wifi)"},
		{" My iPhone (usb)", "udid-phys", " My iPhone (usb) — leased, suspended, session jkl000"},
	}
	for _, tt := range tests {
		if got := AnnotateLabel(ann, tt.label, tt.id); got != tt.want {
			t.Errorf("AnnotateLabel(%q) = %q, want %q", tt.id, got, tt.want)
		}
	}
}
