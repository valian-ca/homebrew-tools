package devicebank

import (
	"reflect"
	"testing"
)

// Captured from `xcrun simctl list devices --json` (trimmed): the bank needs
// shutdown devices too, unlike frn's booted-only probe.
const simctlDevicesJSON = `{
  "devices" : {
    "com.apple.CoreSimulator.SimRuntime.iOS-26-4" : [
      {
        "lastBootedAt" : "2026-06-10T14:00:00Z",
        "dataPath" : "\/Users\/fp\/Library\/Developer\/CoreSimulator\/Devices\/AAA\/data",
        "udid" : "AAA-1111",
        "isAvailable" : true,
        "deviceTypeIdentifier" : "com.apple.CoreSimulator.SimDeviceType.iPhone-17",
        "state" : "Booted",
        "name" : "atelier-ios-1"
      },
      {
        "udid" : "BBB-2222",
        "isAvailable" : true,
        "deviceTypeIdentifier" : "com.apple.CoreSimulator.SimDeviceType.iPhone-17",
        "state" : "Shutdown",
        "name" : "atelier-ios-2"
      }
    ],
    "com.apple.CoreSimulator.SimRuntime.iOS-17-4" : [
      {
        "udid" : "CCC-3333",
        "isAvailable" : true,
        "deviceTypeIdentifier" : "com.apple.CoreSimulator.SimDeviceType.iPhone-15",
        "state" : "Shutdown",
        "name" : "iPhone 15"
      }
    ]
  }
}`

func TestParseSimctlDevicesIncludesShutdown(t *testing.T) {
	sims, err := parseSimctlDevices([]byte(simctlDevicesJSON))
	if err != nil {
		t.Fatal(err)
	}
	want := []Sim{
		{Name: "atelier-ios-1", UDID: "AAA-1111", State: "Booted"},
		{Name: "atelier-ios-2", UDID: "BBB-2222", State: "Shutdown"},
		{Name: "iPhone 15", UDID: "CCC-3333", State: "Shutdown"},
	}
	if !reflect.DeepEqual(sims, want) {
		t.Errorf("parseSimctlDevices() = %+v, want %+v", sims, want)
	}
}

// Captured from `xcrun simctl list devicetypes --json` (trimmed).
const simctlDeviceTypesJSON = `{
  "devicetypes" : [
    {
      "name" : "iPhone 17 Pro Max",
      "identifier" : "com.apple.CoreSimulator.SimDeviceType.iPhone-17-Pro-Max"
    },
    {
      "name" : "iPhone Air",
      "identifier" : "com.apple.CoreSimulator.SimDeviceType.iPhone-Air"
    },
    {
      "name" : "iPhone 17",
      "identifier" : "com.apple.CoreSimulator.SimDeviceType.iPhone-17"
    },
    {
      "name" : "iPhone 16 Pro",
      "identifier" : "com.apple.CoreSimulator.SimDeviceType.iPhone-16-Pro"
    },
    {
      "name" : "iPad Pro 11-inch (M4)",
      "identifier" : "com.apple.CoreSimulator.SimDeviceType.iPad-Pro-11-inch-M4"
    }
  ]
}`

func TestPickIPhoneDeviceTypeNewestBaseModel(t *testing.T) {
	got, err := pickIPhoneDeviceType([]byte(simctlDeviceTypesJSON))
	if err != nil {
		t.Fatal(err)
	}
	// Highest version wins; among 17s the base model (shortest name) wins.
	want := "com.apple.CoreSimulator.SimDeviceType.iPhone-17"
	if got != want {
		t.Errorf("pickIPhoneDeviceType() = %q, want %q", got, want)
	}
}

// Captured from `xcrun simctl list runtimes --json` (trimmed).
const simctlRuntimesJSON = `{
  "runtimes" : [
    {
      "identifier" : "com.apple.CoreSimulator.SimRuntime.iOS-17-4",
      "version" : "17.4",
      "isAvailable" : true,
      "platform" : "iOS"
    },
    {
      "identifier" : "com.apple.CoreSimulator.SimRuntime.iOS-26-4",
      "version" : "26.4.1",
      "isAvailable" : true,
      "platform" : "iOS"
    },
    {
      "identifier" : "com.apple.CoreSimulator.SimRuntime.iOS-26-2",
      "version" : "26.2",
      "isAvailable" : true,
      "platform" : "iOS"
    },
    {
      "identifier" : "com.apple.CoreSimulator.SimRuntime.watchOS-26-4",
      "version" : "99.0",
      "isAvailable" : true,
      "platform" : "watchOS"
    },
    {
      "identifier" : "com.apple.CoreSimulator.SimRuntime.iOS-27-0",
      "version" : "27.0",
      "isAvailable" : false,
      "platform" : "iOS"
    }
  ]
}`

func TestPickIOSRuntimeNewestAvailable(t *testing.T) {
	got, err := pickIOSRuntime([]byte(simctlRuntimesJSON))
	if err != nil {
		t.Fatal(err)
	}
	// 27.0 is unavailable and watchOS 99.0 is the wrong platform.
	want := "com.apple.CoreSimulator.SimRuntime.iOS-26-4"
	if got != want {
		t.Errorf("pickIOSRuntime() = %q, want %q", got, want)
	}
}
