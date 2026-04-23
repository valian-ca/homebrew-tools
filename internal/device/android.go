package device

import (
	"bufio"
	"context"
	"errors"
	"os/exec"
	"strings"
)

// probeAndroid shells out to `adb devices -l` under ctx's deadline. The
// second return is true when adb hung past the deadline; the picker uses
// that to offer a "Restart adb server" action.
func probeAndroid(ctx context.Context) ([]Device, bool) {
	if _, err := exec.LookPath("adb"); err != nil {
		return nil, false
	}
	out, err := exec.CommandContext(ctx, "adb", "devices", "-l").Output()
	if err != nil {
		// Context deadline → adb is hung. Any other error → no devices.
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, true
		}
		return nil, false
	}
	return parseAdbDevices(string(out)), false
}

// parseAdbDevices parses the tabular `adb devices -l` output. First line is
// the "List of devices attached" header; only lines whose second column is
// exactly "device" are real devices (offline/unauthorized are skipped, same
// as the bash version).
func parseAdbDevices(stdout string) []Device {
	var out []Device
	sc := bufio.NewScanner(strings.NewReader(stdout))
	first := true
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if first {
			first = false
			continue
		}
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[1] != "device" {
			continue
		}
		serial := fields[0]
		model := "Android"
		for _, f := range fields[2:] {
			if strings.HasPrefix(f, "model:") {
				model = strings.ReplaceAll(strings.TrimPrefix(f, "model:"), "_", " ")
			}
		}
		var tag string
		var kind Kind
		switch {
		case strings.HasPrefix(serial, "emulator-"):
			tag, kind = "emulator", KindAndroidEmulator
		case strings.Contains(serial, ":"):
			tag, kind = "wifi", KindAndroidWiFi
		default:
			tag, kind = "usb", KindAndroidUSB
		}
		out = append(out, Device{
			Label: glyphAndroid + " " + model + " (" + tag + ")",
			ID:    serial,
			Kind:  kind,
		})
	}
	return out
}
