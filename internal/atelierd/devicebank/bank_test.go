package devicebank

import (
	"reflect"
	"testing"
)

func TestPlanResize(t *testing.T) {
	tests := []struct {
		name    string
		devices []*Device
		n       int
		want    ResizePlan
	}{
		{
			name: "fresh bank creates all",
			n:    2,
			want: ResizePlan{Create: []string{"atelier-ios-1", "atelier-ios-2"}},
		},
		{
			name: "idempotent rerun changes nothing",
			devices: []*Device{
				{Name: "atelier-ios-1", Platform: PlatformIOS, State: StateFree},
				{Name: "atelier-ios-2", Platform: PlatformIOS, State: StateOff},
			},
			n:    2,
			want: ResizePlan{},
		},
		{
			name: "grow fills the gap",
			devices: []*Device{
				{Name: "atelier-ios-1", Platform: PlatformIOS, State: StateFree},
			},
			n:    3,
			want: ResizePlan{Create: []string{"atelier-ios-2", "atelier-ios-3"}},
		},
		{
			name: "shrink deletes free excess, keeps leased with warning",
			devices: []*Device{
				{Name: "atelier-ios-1", Platform: PlatformIOS, State: StateFree},
				{Name: "atelier-ios-2", Platform: PlatformIOS, State: StateLeased},
				{Name: "atelier-ios-3", Platform: PlatformIOS, State: StateOff},
			},
			n:    1,
			want: ResizePlan{Delete: []string{"atelier-ios-3"}, Keep: []string{"atelier-ios-2"}},
		},
		{
			name: "recycling excess is busy, kept like leased",
			devices: []*Device{
				{Name: "atelier-ios-1", Platform: PlatformIOS, State: StateFree},
				{Name: "atelier-ios-2", Platform: PlatformIOS, State: StateRecycling},
			},
			n:    1,
			want: ResizePlan{Keep: []string{"atelier-ios-2"}},
		},
		{
			name: "other platform is untouched",
			devices: []*Device{
				{Name: "atelier-android-1", Platform: PlatformAndroid, State: StateFree},
			},
			n:    1,
			want: ResizePlan{Create: []string{"atelier-ios-1"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := planResize(&State{Devices: tt.devices}, PlatformIOS, tt.n)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("planResize() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestBankIndex(t *testing.T) {
	tests := []struct {
		platform Platform
		name     string
		want     int
	}{
		{PlatformIOS, "atelier-ios-1", 1},
		{PlatformIOS, "atelier-ios-12", 12},
		{PlatformIOS, "atelier-android-1", 0},
		{PlatformIOS, "atelier-ios-0", 0},
		{PlatformIOS, "atelier-ios-x", 0},
		{PlatformIOS, "iPhone 17", 0},
		{PlatformAndroid, "atelier-android-3", 3},
		{PlatformAndroid, "Pixel_7_Pro", 0},
	}
	for _, tt := range tests {
		if got := bankIndex(tt.platform, tt.name); got != tt.want {
			t.Errorf("bankIndex(%s, %q) = %d, want %d", tt.platform, tt.name, got, tt.want)
		}
	}
}

func TestEmulatorPort(t *testing.T) {
	// Descends from the top of adb's auto-discovery range [5554, 5584]:
	// outside it adb never sees the emulator, below it manual emulators
	// climbing from 5554 would collide.
	if got := EmulatorPort(1); got != 5584 {
		t.Errorf("EmulatorPort(1) = %d, want 5584", got)
	}
	if got := EmulatorPort(2); got != 5582 {
		t.Errorf("EmulatorPort(2) = %d, want 5582", got)
	}
}
