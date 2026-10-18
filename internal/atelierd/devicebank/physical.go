package devicebank

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// PhysicalDevice is a plugged device as the bank's own probe sees it. ID is
// what `flutter run -d` targets (the USB transport when there is one); Serial
// identifies the device across transports (ro.serialno, or the UDID).
type PhysicalDevice struct {
	ID       string
	Serial   string
	Name     string
	Platform Platform
	Wired    bool
}

// Name stays empty on Android: the label costs an APK pull (androidLabel), so
// it is read only when it decides between conflict and overwrite.
type AppInfo struct {
	Installed bool
	Developer bool
	Name      string
}

const devBuildNamePrefix = "[D]"

func (a AppInfo) overwritable() bool {
	return !a.Installed || a.Developer || strings.HasPrefix(a.Name, devBuildNamePrefix)
}

type adbTransport struct {
	id    string
	model string
	usb   bool
}

// parseAdbDevicesLong reads `adb devices -l`. Only transports in the device
// state count: unauthorized means USB debugging was never allowed on this
// machine. A usb: field is printed for USB transports only, whatever the
// serial looks like (adb-wifi mDNS names carry no colon).
func parseAdbDevicesLong(out []byte) []adbTransport {
	var ts []adbTransport
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] == "List" || strings.HasPrefix(fields[0], "*") {
			continue
		}
		if fields[1] != "device" || strings.HasPrefix(fields[0], "emulator-") {
			continue
		}
		t := adbTransport{id: fields[0], model: "Android"}
		for _, f := range fields[2:] {
			switch {
			case strings.HasPrefix(f, "usb:"):
				t.usb = true
			case strings.HasPrefix(f, "model:"):
				t.model = strings.ReplaceAll(strings.TrimPrefix(f, "model:"), "_", " ")
			}
		}
		ts = append(ts, t)
	}
	return ts
}

// mdnsSerial extracts <serialno> from an adb-wifi mDNS transport name,
// adb-<serialno>-<suffix>._adb-tls-connect._tcp.
func mdnsSerial(id string) string {
	name, _, found := strings.Cut(id, "._adb-tls-")
	if !found || !strings.HasPrefix(name, "adb-") {
		return ""
	}
	name = strings.TrimPrefix(name, "adb-")
	if i := strings.LastIndex(name, "-"); i > 0 {
		return name[:i]
	}
	return name
}

func groupBySerial(ts []adbTransport, serialOf func(id string) string) []PhysicalDevice {
	bySerial := map[string]*PhysicalDevice{}
	var order []string
	for _, t := range ts {
		serial := serialOf(t.id)
		if serial == "" {
			serial = mdnsSerial(t.id)
		}
		if serial == "" {
			serial = t.id
		}
		p, ok := bySerial[serial]
		if !ok {
			p = &PhysicalDevice{ID: t.id, Serial: serial, Name: t.model, Platform: PlatformAndroid}
			bySerial[serial] = p
			order = append(order, serial)
		}
		if t.usb && !p.Wired {
			p.ID, p.Wired = t.id, true
		}
	}
	out := make([]PhysicalDevice, 0, len(order))
	for _, serial := range order {
		out = append(out, *bySerial[serial])
	}
	return out
}

type devicectlDevices struct {
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
				PairingState string `json:"pairingState"`
				TunnelState  string `json:"tunnelState"`
			} `json:"connectionProperties"`
		} `json:"devices"`
	} `json:"result"`
}

var usbSerialNumber = regexp.MustCompile(`"USB Serial Number" = "([^"]+)"`)

func parseIoregSerials(out []byte) map[string]bool {
	serials := map[string]bool{}
	for _, m := range usbSerialNumber.FindAllSubmatch(out, -1) {
		serials[strings.ToUpper(string(m[1]))] = true
	}
	return serials
}

// parseIOSPhysical crosses devicectl with the USB bus. devicectl's
// transportType is the last known transport — it says wired for an iPhone
// unplugged since — so only the bus tells whether the cable is there: an
// iPhone's USB serial number is its UDID without the hyphen.
func parseIOSPhysical(devicectlJSON, ioreg []byte) ([]PhysicalDevice, error) {
	var parsed devicectlDevices
	if err := json.Unmarshal(devicectlJSON, &parsed); err != nil {
		return nil, err
	}
	onBus := parseIoregSerials(ioreg)
	var out []PhysicalDevice
	for _, d := range parsed.Result.Devices {
		c := d.ConnectionProperties
		if d.HardwareProperties.Platform != "iOS" || c.PairingState != "paired" || c.TunnelState == "unavailable" {
			continue
		}
		udid := d.HardwareProperties.UDID
		out = append(out, PhysicalDevice{
			ID: udid, Serial: udid, Name: d.DeviceProperties.Name, Platform: PlatformIOS,
			Wired: onBus[strings.ToUpper(strings.ReplaceAll(udid, "-", ""))],
		})
	}
	return out, nil
}

var physicalToolEnv []string

// runBounded returns stdout alone: a device's shell prints warnings on
// stderr, and a value read here may be written back to the device later.
func runBounded(ctx context.Context, name string, args ...string) ([]byte, error) {
	execCtx, cancel := boundedCtx(ctx)
	defer cancel()
	cmd := exec.CommandContext(execCtx, name, args...)
	cmd.Env = physicalToolEnv
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()+" "+string(out)))
	}
	return out, nil
}

func adbShell(ctx context.Context, id string, args ...string) (string, error) {
	out, err := runBounded(ctx, "adb", append([]string{"-s", id, "shell"}, args...)...)
	return strings.TrimSpace(string(out)), err
}

func probeAndroidPhysical(ctx context.Context) ([]PhysicalDevice, error) {
	out, err := runBounded(ctx, "adb", "devices", "-l")
	if err != nil {
		return nil, err
	}
	return groupBySerial(parseAdbDevicesLong(out), func(id string) string {
		serial, err := adbShell(ctx, id, "getprop", "ro.serialno")
		if err != nil {
			return ""
		}
		return serial
	}), nil
}

// devicectlJSON runs a devicectl command that only writes JSON to a file.
func devicectlJSON(ctx context.Context, args ...string) ([]byte, error) {
	tmp, err := os.CreateTemp("", "atelierd-devicectl-*.json")
	if err != nil {
		return nil, err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	if _, err := runBounded(ctx, "xcrun", append(append([]string{"devicectl"}, args...), "--json-output", tmp.Name())...); err != nil {
		return nil, err
	}
	return os.ReadFile(tmp.Name())
}

func probeIOSPhysical(ctx context.Context) ([]PhysicalDevice, error) {
	devices, err := devicectlJSON(ctx, "list", "devices")
	if err != nil {
		return nil, err
	}
	bus, err := runBounded(ctx, "ioreg", "-p", "IOUSB", "-l", "-w0")
	if err != nil {
		return nil, err
	}
	return parseIOSPhysical(devices, bus)
}

const allPlatforms Platform = ""

func probePhysical(ctx context.Context, platform Platform) ([]PhysicalDevice, error) {
	var out []PhysicalDevice
	var errs []error
	if platform == allPlatforms || platform == PlatformAndroid {
		ds, err := probeAndroidPhysical(ctx)
		out, errs = append(out, ds...), append(errs, err)
	}
	if platform == allPlatforms || platform == PlatformIOS {
		ds, err := probeIOSPhysical(ctx)
		out, errs = append(out, ds...), append(errs, err)
	}
	return out, errors.Join(errs...)
}

const stayOnSetting = "stay_on_while_plugged_in"

var stayOnValue = regexp.MustCompile(`^(null|-?[0-9]+)$`)

func readStayOn(ctx context.Context, id string) (string, error) {
	v, err := adbShell(ctx, id, "settings", "get", "global", stayOnSetting)
	if err != nil {
		return "", err
	}
	if !stayOnValue.MatchString(v) {
		return "", fmt.Errorf("unexpected %s value %q", stayOnSetting, v)
	}
	return v, nil
}

// writeStayOn puts back a recorded value; "null" was an unset key, which
// only a delete restores.
func writeStayOn(ctx context.Context, id, value string) error {
	if value == "null" {
		_, err := adbShell(ctx, id, "settings", "delete", "global", stayOnSetting)
		return err
	}
	_, err := adbShell(ctx, id, "settings", "put", "global", stayOnSetting, value)
	return err
}

func removeAdbReverses(ctx context.Context, id string) error {
	_, err := runBounded(ctx, "adb", "-s", id, "reverse", "--remove-all")
	return err
}

func parsePmPackages(out string) []string {
	var ids []string
	for _, line := range strings.Split(out, "\n") {
		if id, ok := strings.CutPrefix(strings.TrimSpace(line), "package:"); ok && id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

type devicectlApps struct {
	Result struct {
		Apps []struct {
			BundleIdentifier string `json:"bundleIdentifier"`
			Name             string `json:"name"`
			BuiltByDeveloper bool   `json:"builtByDeveloper"`
		} `json:"apps"`
	} `json:"result"`
}

// iosApps lists every app: devicectl's default lists developer apps only,
// which would hide a Store app under the run's bundle id.
func iosApps(ctx context.Context, udid string) (map[string]AppInfo, error) {
	data, err := devicectlJSON(ctx, "device", "info", "apps", "--device", udid, "--include-all-apps")
	if err != nil {
		return nil, err
	}
	var parsed devicectlApps
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	apps := map[string]AppInfo{}
	for _, a := range parsed.Result.Apps {
		apps[a.BundleIdentifier] = AppInfo{Installed: true, Developer: a.BuiltByDeveloper, Name: a.Name}
	}
	return apps, nil
}

func installedApps(ctx context.Context, p PhysicalDevice) ([]string, error) {
	if p.Platform == PlatformIOS {
		apps, err := iosApps(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(apps))
		for id := range apps {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		return ids, nil
	}
	out, err := adbShell(ctx, p.ID, "pm", "list", "packages")
	if err != nil {
		return nil, err
	}
	return parsePmPackages(out), nil
}

var pkgFlags = regexp.MustCompile(`pkgFlags=\[([^\]]*)\]`)

func parseDebuggable(dumpsys string) (bool, error) {
	m := pkgFlags.FindStringSubmatch(dumpsys)
	if m == nil {
		return false, errors.New("no pkgFlags in dumpsys package")
	}
	return strings.Contains(" "+m[1]+" ", " DEBUGGABLE "), nil
}

var applicationLabel = regexp.MustCompile(`(?m)^application-label:'(.*)'$`)

func parseApplicationLabel(badging string) (string, error) {
	m := applicationLabel.FindStringSubmatch(badging)
	if m == nil {
		return "", errors.New("no application-label in aapt2 badging")
	}
	return m[1], nil
}

func aapt2Path() (string, error) {
	matches, _ := filepath.Glob(filepath.Join(androidSDKRoot(), "build-tools", "*", "aapt2"))
	sort.Slice(matches, func(i, j int) bool {
		return versionLess(parseVersion(filepath.Base(filepath.Dir(matches[j]))), parseVersion(filepath.Base(filepath.Dir(matches[i]))))
	})
	if len(matches) > 0 {
		return matches[0], nil
	}
	return exec.LookPath("aapt2")
}

// androidLabel reads the launcher label from the installed base APK: no
// shell command prints it.
func androidLabel(ctx context.Context, id, pkg string) (string, error) {
	paths, err := adbShell(ctx, id, "pm", "path", pkg)
	if err != nil {
		return "", err
	}
	var apk string
	for _, p := range parsePmPackages(paths) {
		if apk == "" || strings.HasSuffix(p, "/base.apk") {
			apk = p
		}
	}
	if apk == "" {
		return "", fmt.Errorf("pm path %s printed no APK", pkg)
	}
	dir, err := os.MkdirTemp("", "atelierd-apk-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	local := filepath.Join(dir, "base.apk")
	if _, err := runBounded(ctx, "adb", "-s", id, "pull", apk, local); err != nil {
		return "", err
	}
	aapt2, err := aapt2Path()
	if err != nil {
		return "", err
	}
	out, err := runBounded(ctx, aapt2, "dump", "badging", local)
	if err != nil {
		return "", err
	}
	return parseApplicationLabel(string(out))
}

func appInfo(ctx context.Context, p PhysicalDevice, appID string) (AppInfo, error) {
	if p.Platform == PlatformIOS {
		apps, err := iosApps(ctx, p.ID)
		if err != nil {
			return AppInfo{}, err
		}
		return apps[appID], nil
	}
	ids, err := installedApps(ctx, p)
	if err != nil {
		return AppInfo{}, err
	}
	if !slices.Contains(ids, appID) {
		return AppInfo{}, nil
	}
	dump, err := adbShell(ctx, p.ID, "dumpsys", "package", appID)
	if err != nil {
		return AppInfo{}, err
	}
	debuggable, err := parseDebuggable(dump)
	if err != nil {
		return AppInfo{}, err
	}
	return AppInfo{Installed: true, Developer: debuggable}, nil
}

func uninstallApp(ctx context.Context, p PhysicalDevice, appID string) error {
	if p.Platform == PlatformIOS {
		_, err := runBounded(ctx, "xcrun", "devicectl", "device", "uninstall", "app", "--device", p.ID, appID)
		return err
	}
	_, err := runBounded(ctx, "adb", "-s", p.ID, "uninstall", appID)
	return err
}
