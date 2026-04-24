package device

import (
	"testing"
)

func TestParseAdbDevicesMixed(t *testing.T) {
	input := `List of devices attached
2B021FDH3005LU         device usb:1-3 product:panther model:Pixel_7 device:panther transport_id:1
192.168.1.50:5555      device product:panther model:Pixel_7_Wifi device:panther
emulator-5554          device product:sdk_gphone64_arm64 model:sdk_gphone64_arm64 device:emu64a
OFFLINEID              offline
`
	got := parseAdbDevices(input)
	if len(got) != 3 {
		t.Fatalf("expected 3 devices, got %d: %+v", len(got), got)
	}
	if got[0].Kind != KindAndroidUSB {
		t.Errorf("device 0 kind: got %v, want USB", got[0].Kind)
	}
	if got[0].ID != "2B021FDH3005LU" {
		t.Errorf("device 0 id: got %q", got[0].ID)
	}
	if got[1].Kind != KindAndroidWiFi {
		t.Errorf("device 1 kind: got %v, want WiFi", got[1].Kind)
	}
	if got[2].Kind != KindAndroidEmulator {
		t.Errorf("device 2 kind: got %v, want Emulator", got[2].Kind)
	}
	// Underscore in model should be replaced with space.
	if want := "Pixel 7"; !contains(got[0].Label, want) {
		t.Errorf("device 0 label %q should contain %q", got[0].Label, want)
	}
	if !contains(got[0].Label, "(usb)") {
		t.Errorf("device 0 label missing usb tag: %q", got[0].Label)
	}
	if !contains(got[1].Label, "(wifi)") {
		t.Errorf("device 1 label missing wifi tag: %q", got[1].Label)
	}
	if !contains(got[2].Label, "(emulator)") {
		t.Errorf("device 2 label missing emulator tag: %q", got[2].Label)
	}
}

func TestParseAdbDevicesEmpty(t *testing.T) {
	got := parseAdbDevices("List of devices attached\n\n")
	if len(got) != 0 {
		t.Fatalf("expected no devices, got %+v", got)
	}
}

func TestIsPhysical(t *testing.T) {
	cases := []struct {
		k    Kind
		want bool
	}{
		{KindAndroidUSB, true},
		{KindAndroidWiFi, true},
		{KindIOS, true},
		{KindAndroidEmulator, false},
		{KindSimulator, false},
	}
	for _, c := range cases {
		if got := (Device{Kind: c.k}).IsPhysical(); got != c.want {
			t.Errorf("kind %v: got %v, want %v", c.k, got, c.want)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// Real devicectl output captured from an iPhone 12 Mini that was paired
// but not currently connected. tunnelState=unavailable is the tell.
const devicectlUnavailableJSON = `{
  "info": {"outcome": "success"},
  "result": {
    "devices": [
      {
        "connectionProperties": {
          "pairingState": "paired",
          "tunnelState": "unavailable"
        },
        "deviceProperties": {"name": "iPhone 12 Mini"},
        "hardwareProperties": {
          "platform": "iOS",
          "udid": "00008101-0011649C3EB8001E"
        }
      }
    ]
  }
}`

// A synthesised "connected" device — wired, tunnel up, paired.
const devicectlConnectedJSON = `{
  "result": {
    "devices": [
      {
        "connectionProperties": {
          "pairingState": "paired",
          "tunnelState": "connected",
          "transportType": "wired"
        },
        "deviceProperties": {"name": "iPhone 15"},
        "hardwareProperties": {
          "platform": "iOS",
          "udid": "AAAA-BBBB"
        }
      }
    ]
  }
}`

func TestParseDevicectlFiltersUnavailable(t *testing.T) {
	got, err := parseDevicectl([]byte(devicectlUnavailableJSON))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected unavailable device to be filtered out, got %+v", got)
	}
}

func TestParseDevicectlIncludesConnected(t *testing.T) {
	got, err := parseDevicectl([]byte(devicectlConnectedJSON))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 device, got %d: %+v", len(got), got)
	}
	if got[0].ID != "AAAA-BBBB" {
		t.Errorf("ID: got %q", got[0].ID)
	}
	if !contains(got[0].Label, "iPhone 15") {
		t.Errorf("Label missing name: %q", got[0].Label)
	}
	if !contains(got[0].Label, "(wired)") {
		t.Errorf("Label missing transport: %q", got[0].Label)
	}
}

func TestParseDevicectlSkipsUnpaired(t *testing.T) {
	payload := `{"result":{"devices":[{
    "connectionProperties":{"pairingState":"unpaired","tunnelState":"connected"},
    "deviceProperties":{"name":"x"},
    "hardwareProperties":{"platform":"iOS","udid":"u"}
  }]}}`
	got, err := parseDevicectl([]byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("unpaired device should be filtered, got %+v", got)
	}
}

// Real capture: paired iPhone with the tunnel idle. devicectl's CLI shows
// this as `available (paired)` — CoreDevice brings the tunnel up on demand
// when flutter run targets the UDID, so we must not filter it.
func TestParseDevicectlIncludesAvailableDisconnected(t *testing.T) {
	payload := `{"result":{"devices":[{
    "connectionProperties":{"pairingState":"paired","tunnelState":"disconnected","transportType":"wired"},
    "deviceProperties":{"name":"iPhone 12 Mini"},
    "hardwareProperties":{"platform":"iOS","udid":"00008101-0011649C3EB8001E"}
  }]}}`
	got, err := parseDevicectl([]byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected available-but-disconnected device to be included, got %+v", got)
	}
	if got[0].ID != "00008101-0011649C3EB8001E" {
		t.Errorf("ID: got %q", got[0].ID)
	}
}

func TestParseDevicectlConnectedButMissingTransport(t *testing.T) {
	// Sanity: if transportType is absent on a connected device, we still
	// emit it with a placeholder label rather than dropping it.
	payload := `{"result":{"devices":[{
    "connectionProperties":{"pairingState":"paired","tunnelState":"connected"},
    "deviceProperties":{"name":"iPad"},
    "hardwareProperties":{"platform":"iOS","udid":"u"}
  }]}}`
	got, err := parseDevicectl([]byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !contains(got[0].Label, "iPad") {
		t.Fatalf("expected connected iPad, got %+v", got)
	}
}
