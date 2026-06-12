package devicebank

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

// unsetEnv clears key for the test body. t.Setenv first registers the
// ambient value for restore at cleanup; the os.Unsetenv is the load-bearing
// clear — an empty-valued var still counts as present to androidEnv's
// HasPrefix check, so t.Setenv alone would not trigger the re-pin.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	os.Unsetenv(key)
}

func TestAndroidEnvPinsAndRespectsOverrides(t *testing.T) {
	t.Run("pins both when absent", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		unsetEnv(t, "ANDROID_USER_HOME")
		unsetEnv(t, "ANDROID_AVD_HOME")

		env := androidEnv()
		envMap := make(map[string]string)
		for _, e := range env {
			parts := strings.SplitN(e, "=", 2)
			if len(parts) == 2 {
				envMap[parts[0]] = parts[1]
			}
		}

		wantUserHome := filepath.Join(home, ".android")
		wantAVDHome := filepath.Join(home, ".android", "avd")

		if envMap["ANDROID_USER_HOME"] != wantUserHome {
			t.Errorf("ANDROID_USER_HOME = %q, want %q", envMap["ANDROID_USER_HOME"], wantUserHome)
		}
		if envMap["ANDROID_AVD_HOME"] != wantAVDHome {
			t.Errorf("ANDROID_AVD_HOME = %q, want %q", envMap["ANDROID_AVD_HOME"], wantAVDHome)
		}
	})

	t.Run("respects caller override", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		customUserHome := "/custom"
		t.Setenv("ANDROID_USER_HOME", customUserHome)
		unsetEnv(t, "ANDROID_AVD_HOME")

		env := androidEnv()
		envMap := make(map[string]string)
		for _, e := range env {
			parts := strings.SplitN(e, "=", 2)
			if len(parts) == 2 {
				envMap[parts[0]] = parts[1]
			}
		}

		if envMap["ANDROID_USER_HOME"] != customUserHome {
			t.Errorf("ANDROID_USER_HOME = %q, want %q", envMap["ANDROID_USER_HOME"], customUserHome)
		}

		wantAVDHome := filepath.Join(home, ".android", "avd")
		if envMap["ANDROID_AVD_HOME"] != wantAVDHome {
			t.Errorf("ANDROID_AVD_HOME = %q, want %q", envMap["ANDROID_AVD_HOME"], wantAVDHome)
		}
	})
}

func TestSdkToolPrefersSDKOverPath(t *testing.T) {
	t.Run("prefers SDK location over PATH", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv("ANDROID_HOME", root)

		cmdlineToolsPath := filepath.Join(root, "cmdline-tools", "latest", "bin")
		if err := os.MkdirAll(cmdlineToolsPath, 0o755); err != nil {
			t.Fatal(err)
		}
		avdmanagerPath := filepath.Join(cmdlineToolsPath, "avdmanager")
		if err := os.WriteFile(avdmanagerPath, []byte("fake"), 0o755); err != nil {
			t.Fatal(err)
		}

		got, err := sdkTool("avdmanager", "cmdline-tools/latest/bin", "tools/bin")
		if err != nil {
			t.Fatal(err)
		}
		if got != avdmanagerPath {
			t.Errorf("sdkTool() = %q, want %q", got, avdmanagerPath)
		}
	})

	t.Run("first relative directory wins", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv("ANDROID_HOME", root)

		cmdlineToolsPath := filepath.Join(root, "cmdline-tools", "latest", "bin")
		if err := os.MkdirAll(cmdlineToolsPath, 0o755); err != nil {
			t.Fatal(err)
		}
		cmdlineAvdmanagerPath := filepath.Join(cmdlineToolsPath, "avdmanager")
		if err := os.WriteFile(cmdlineAvdmanagerPath, []byte("fake"), 0o755); err != nil {
			t.Fatal(err)
		}

		toolsPath := filepath.Join(root, "tools", "bin")
		if err := os.MkdirAll(toolsPath, 0o755); err != nil {
			t.Fatal(err)
		}
		toolsAvdmanagerPath := filepath.Join(toolsPath, "avdmanager")
		if err := os.WriteFile(toolsAvdmanagerPath, []byte("fake"), 0o755); err != nil {
			t.Fatal(err)
		}

		got, err := sdkTool("avdmanager", "cmdline-tools/latest/bin", "tools/bin")
		if err != nil {
			t.Fatal(err)
		}
		if got != cmdlineAvdmanagerPath {
			t.Errorf("sdkTool() = %q, want %q (cmdline-tools should win)", got, cmdlineAvdmanagerPath)
		}
	})

	t.Run("not found returns error", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv("ANDROID_HOME", root)
		t.Setenv("PATH", "")

		_, err := sdkTool("definitely-not-a-real-tool-xyz", "cmdline-tools/latest/bin")
		if err == nil {
			t.Errorf("sdkTool() = nil, want error")
		}
	})
}
