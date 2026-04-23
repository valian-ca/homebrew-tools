package device

import (
	"context"
	"encoding/json"
	"os/exec"
)

// xcdeviceEntry is a single element in `xcrun xcdevice list` JSON output.
// xcdevice actively probes USB/Wi-Fi, so it sees devices CoreDevice's
// cache misses — at the cost of always waiting near the full --timeout.
type xcdeviceEntry struct {
	Simulator  bool   `json:"simulator"`
	Available  bool   `json:"available"`
	Platform   string `json:"platform"`
	Interface  string `json:"interface"` // "usb" or "wifi"
	Name       string `json:"name"`
	Identifier string `json:"identifier"`
}

// DeepProbeIOS runs `xcrun xcdevice list --timeout 2` and returns the
// usable physical iOS devices. Slow (~2s wall-clock) because xcdevice
// always waits near the full timeout regardless of what it finds — so
// this is meant to run in parallel with the fast probes, not synchronously.
func DeepProbeIOS(ctx context.Context) []Device {
	if _, err := exec.LookPath("xcrun"); err != nil {
		return nil
	}
	out, err := exec.CommandContext(ctx, "xcrun", "xcdevice", "list", "--timeout", "2").Output()
	if err != nil {
		return nil
	}
	return parseXCDevice(out)
}

// parseXCDevice extracts connected iOS devices from the top-level JSON
// array that `xcdevice list` emits. Filters: not a simulator, marked
// available, and iPhone/iPad platform (skip macOS hosts).
func parseXCDevice(data []byte) []Device {
	var entries []xcdeviceEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil
	}
	var out []Device
	for _, e := range entries {
		if e.Simulator || !e.Available {
			continue
		}
		if e.Platform != "com.apple.platform.iphoneos" {
			continue
		}
		transport := e.Interface
		if transport == "" {
			transport = "iOS"
		}
		out = append(out, Device{
			Label: glyphApple + " " + e.Name + " (" + transport + ")",
			ID:    e.Identifier,
			Kind:  KindIOS,
		})
	}
	return out
}

// MergeIOS returns r with iosDevices merged in: entries with UDIDs already
// in r.Physical are skipped (devicectl wins — its label format is more
// informative), new ones are appended. xcdevice is authoritative for
// "is this device currently usable"; devicectl's stale entries would have
// been filtered by parseDevicectl already.
func MergeIOS(r Result, iosDevices []Device) Result {
	seen := make(map[string]struct{}, len(r.Physical))
	for _, d := range r.Physical {
		seen[d.ID] = struct{}{}
	}
	for _, d := range iosDevices {
		if _, dup := seen[d.ID]; dup {
			continue
		}
		r.Physical = append(r.Physical, d)
		seen[d.ID] = struct{}{}
	}
	return r
}
