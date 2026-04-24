package device

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
)

// devicectl writes JSON to a file (no stdout mode). We create a temp file,
// pass it via --json-output, then read it back.
type devicectlOutput struct {
	Result struct {
		Devices []struct {
			DeviceProperties struct {
				Name string `json:"name"`
			} `json:"deviceProperties"`
			HardwareProperties struct {
				Platform string `json:"platform"`
				UDID     string `json:"udid"`
			} `json:"hardwareProperties"`
			ConnectionProperties struct {
				PairingState  string `json:"pairingState"`
				TransportType string `json:"transportType"`
				TunnelState   string `json:"tunnelState"`
			} `json:"connectionProperties"`
		} `json:"devices"`
	} `json:"result"`
}

// parseDevicectl extracts the usable paired iOS devices from devicectl's
// JSON output. A device shows up in devicectl's list for a long time after
// it was last paired — we want to include any paired device that CoreDevice
// could bring up on demand. Observed tunnelStates: "connected" (tunnel up),
// "disconnected" (tunnel idle but device joinable — devicectl's CLI reports
// these as `available (paired)`), and "unavailable" (device out of reach).
// Only "unavailable" is filtered; `flutter run -d <udid>` transparently
// establishes the tunnel for the other two.
func parseDevicectl(data []byte) ([]Device, error) {
	var parsed devicectlOutput
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	var out []Device
	for _, d := range parsed.Result.Devices {
		if d.HardwareProperties.Platform != "iOS" {
			continue
		}
		if d.ConnectionProperties.PairingState != "paired" {
			continue
		}
		if d.ConnectionProperties.TunnelState == "unavailable" {
			continue
		}
		transport := d.ConnectionProperties.TransportType
		if transport == "" {
			transport = "iOS"
		}
		out = append(out, Device{
			Label: glyphApple + " " + d.DeviceProperties.Name + " (" + transport + ")",
			ID:    d.HardwareProperties.UDID,
			Kind:  KindIOS,
		})
	}
	return out, nil
}

func probeIOS(ctx context.Context) ([]Device, bool) {
	if _, err := exec.LookPath("xcrun"); err != nil {
		return nil, false
	}
	tmp, err := os.CreateTemp("", "frn-devicectl-*.json")
	if err != nil {
		return nil, false
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	cmd := exec.CommandContext(ctx, "xcrun", "devicectl", "list", "devices", "--json-output", tmp.Name())
	if err := cmd.Run(); err != nil {
		return nil, false
	}
	data, err := os.ReadFile(filepath.Clean(tmp.Name()))
	if err != nil {
		return nil, false
	}
	devices, err := parseDevicectl(data)
	if err != nil {
		return nil, false
	}
	return devices, false
}

// simctl JSON shape: { "devices": { "<runtime>": [ {name, udid, state, ...}, ... ] } }.
type simctlOutput struct {
	Devices map[string][]struct {
		Name  string `json:"name"`
		UDID  string `json:"udid"`
		State string `json:"state"`
	} `json:"devices"`
}

func probeSimulators(ctx context.Context) ([]Device, bool) {
	if _, err := exec.LookPath("xcrun"); err != nil {
		return nil, false
	}
	out, err := exec.CommandContext(ctx, "xcrun", "simctl", "list", "devices", "booted", "--json").Output()
	if err != nil {
		return nil, false
	}
	var parsed simctlOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, false
	}
	var devs []Device
	for _, list := range parsed.Devices {
		for _, d := range list {
			if d.State != "Booted" {
				continue
			}
			devs = append(devs, Device{
				Label: glyphApple + " " + d.Name + " (simulator)",
				ID:    d.UDID,
				Kind:  KindSimulator,
			})
		}
	}
	return devs, false
}
