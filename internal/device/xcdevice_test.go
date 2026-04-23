package device

import (
	"testing"
)

// Synthesised fragment of `xcrun xcdevice list` JSON. Matches the shape
// observed live on macOS 26 / Xcode 26: a top-level array where each entry
// is one device (simulator OR physical OR the host Mac).
const xcdeviceSampleJSON = `[
  {
    "simulator": true,
    "available": true,
    "platform": "com.apple.platform.iphonesimulator",
    "name": "iPhone 16e",
    "identifier": "SIM-UDID"
  },
  {
    "simulator": false,
    "available": false,
    "platform": "com.apple.platform.iphoneos",
    "interface": "usb",
    "name": "iPhone de Rose",
    "identifier": "cc9465e17f5ee3db3e7b46cfc99598ced1de4a2d",
    "error": {"description": "iPhone de Rose is not connected"}
  },
  {
    "simulator": false,
    "available": true,
    "platform": "com.apple.platform.macosx",
    "interface": "usb",
    "name": "My Mac",
    "identifier": "00006020-001A009A1498C01E"
  },
  {
    "simulator": false,
    "available": true,
    "platform": "com.apple.platform.iphoneos",
    "interface": "usb",
    "name": "iPhone 12 Mini",
    "identifier": "00008101-0011649C3EB8001E"
  }
]`

func TestParseXCDeviceKeepsOnlyAvailablePhysicalIOS(t *testing.T) {
	got := parseXCDevice([]byte(xcdeviceSampleJSON))
	if len(got) != 1 {
		t.Fatalf("expected 1 device, got %d: %+v", len(got), got)
	}
	if got[0].ID != "00008101-0011649C3EB8001E" {
		t.Errorf("got ID %q, want the iPhone 12 Mini", got[0].ID)
	}
	if got[0].Kind != KindIOS {
		t.Errorf("got kind %v, want KindIOS", got[0].Kind)
	}
	if !contains(got[0].Label, "iPhone 12 Mini") || !contains(got[0].Label, "(usb)") {
		t.Errorf("unexpected label: %q", got[0].Label)
	}
}

func TestParseXCDeviceEmpty(t *testing.T) {
	if got := parseXCDevice([]byte(`[]`)); len(got) != 0 {
		t.Fatalf("expected no devices, got %+v", got)
	}
}

func TestParseXCDeviceInvalidJSON(t *testing.T) {
	if got := parseXCDevice([]byte(`not json`)); got != nil {
		t.Fatalf("expected nil on invalid JSON, got %+v", got)
	}
}

func TestMergeIOSDedupes(t *testing.T) {
	existing := Device{ID: "SAME", Label: "from-devicectl", Kind: KindIOS}
	incoming := Device{ID: "SAME", Label: "from-xcdevice", Kind: KindIOS}
	fresh := Device{ID: "FRESH", Label: "new-one", Kind: KindIOS}
	r := Result{Physical: []Device{existing}}

	merged := MergeIOS(r, []Device{incoming, fresh})
	if len(merged.Physical) != 2 {
		t.Fatalf("expected 2 physical devices, got %d: %+v", len(merged.Physical), merged.Physical)
	}
	// devicectl entry should win for the duplicate (retained at index 0).
	if merged.Physical[0].Label != "from-devicectl" {
		t.Errorf("expected devicectl label to win, got %q", merged.Physical[0].Label)
	}
	if merged.Physical[1].ID != "FRESH" {
		t.Errorf("expected fresh device appended, got %+v", merged.Physical[1])
	}
}

func TestMergeIOSNoopWhenEmpty(t *testing.T) {
	r := Result{Physical: []Device{{ID: "X"}}}
	merged := MergeIOS(r, nil)
	if len(merged.Physical) != 1 || merged.Physical[0].ID != "X" {
		t.Fatalf("merge with nil should be a no-op, got %+v", merged.Physical)
	}
}
