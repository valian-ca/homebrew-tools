package devicebank

import (
	"testing"
	"time"
)

func TestLeaseAnnotationsComposition(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if got := LeaseAnnotations(); got != nil {
		t.Fatalf("no bank file must mean no annotations, got %v", got)
	}

	err := WithLock(func(s *State) error {
		now := time.Now()
		s.Config = Config{IOS: 2}
		s.Devices = []*Device{
			{Name: "atelier-ios-1", Platform: PlatformIOS, UDID: "AAA-1111", State: StateFree, LastUsedAt: now},
			{Name: "atelier-ios-2", Platform: PlatformIOS, UDID: "BBB-2222", State: StateLeased, LastUsedAt: now},
			{Name: "atelier-android-1", Platform: PlatformAndroid, AVD: "atelier-android-1", Port: 5642, State: StateRecycling, LastUsedAt: now},
		}
		s.Leases = []*Lease{
			{SessionID: "abc123", Platform: PlatformIOS, DeviceName: "atelier-ios-2", DeviceID: "BBB-2222", AcquiredAt: now, RenewedAt: now},
			{SessionID: "def456", Platform: PlatformAndroid, DeviceID: "serial-9", DeviceName: "Pixel", Physical: true, AcquiredAt: now, RenewedAt: now},
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	ann := LeaseAnnotations()
	tests := []struct {
		label, id, want string
	}{
		{" atelier-ios-1 (simulator)", "AAA-1111", " atelier-ios-1 (simulator) — bank, free"},
		{" atelier-ios-2 (simulator)", "BBB-2222", " atelier-ios-2 (simulator) — bank, leased, session abc123"},
		{" Android (emulator)", "emulator-5642", " Android (emulator) — bank, recycling"},
		{" Pixel 7 (usb)", "serial-9", " Pixel 7 (usb) — leased, session def456"},
		{" My iPhone (wifi)", "unrelated-udid", " My iPhone (wifi)"},
	}
	for _, tt := range tests {
		if got := AnnotateLabel(ann, tt.label, tt.id); got != tt.want {
			t.Errorf("AnnotateLabel(%q) = %q, want %q", tt.id, got, tt.want)
		}
	}
}
