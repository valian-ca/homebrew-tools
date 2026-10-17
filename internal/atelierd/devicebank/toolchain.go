package devicebank

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Boot/health deadline. simctl bootstatus and a cold AVD boot both sit well
// under this on a healthy machine; past it the device is treated as wedged.
const bootTimeout = 3 * time.Minute

// cmdTimeout bounds the quick toolchain commands (list, create, delete,
// shutdown, erase, kill). Distinct from bootTimeout: a boot legitimately
// takes minutes, a list/delete that does is wedged.
const cmdTimeout = 30 * time.Second

func boundedCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, cmdTimeout)
}

func HasXcode() bool {
	_, err := exec.LookPath("xcrun")
	return err == nil
}

// androidSDKRoot resolves the SDK root from the conventional env vars,
// falling back to the macOS default install location.
func androidSDKRoot() string {
	for _, env := range []string{"ANDROID_HOME", "ANDROID_SDK_ROOT"} {
		if v := os.Getenv(env); v != "" {
			return v
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "Android", "sdk")
}

// sdkTool finds an SDK binary, preferring the SDK's own locations over the
// PATH: a PATH shim often points at the legacy tools/bin generation (whose
// avdmanager crashes on Java 9+, JAXB removed) while cmdline-tools/latest
// carries the working one.
func sdkTool(name string, sdkRelative ...string) (string, error) {
	root := androidSDKRoot()
	for _, rel := range sdkRelative {
		p := filepath.Join(root, rel, name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("%s not found under %s or in PATH", name, root)
}

func avdmanagerPath() (string, error) {
	return sdkTool("avdmanager", "cmdline-tools/latest/bin", "tools/bin")
}

func emulatorPath() (string, error) {
	return sdkTool("emulator", "emulator")
}

func HasAndroidSDK() bool {
	if _, err := avdmanagerPath(); err != nil {
		return false
	}
	_, err := emulatorPath()
	return err == nil
}

// androidEnv pins ANDROID_USER_HOME and ANDROID_AVD_HOME for every
// AVD-touching subprocess. avdmanager (Java) resolves its home via getpwuid
// and ignores $HOME — recent versions default to $user.home/.config/.android
// and disregard even ANDROID_AVD_HOME — while emulator (C++) honors $HOME.
// Without both pins the two tools read different AVD directories and the
// bank loses track of its own clones. $HOME/.android is the historical
// location pre-existing AVDs on team machines already live in.
func androidEnv() []string {
	env := os.Environ()
	home, err := os.UserHomeDir()
	if err != nil {
		return env
	}
	userHome := filepath.Join(home, ".android")
	for _, pin := range []struct{ key, val string }{
		{"ANDROID_USER_HOME", userHome},
		{"ANDROID_AVD_HOME", filepath.Join(userHome, "avd")},
	} {
		present := false
		for _, e := range env {
			if strings.HasPrefix(e, pin.key+"=") {
				present = true
				break
			}
		}
		if !present {
			env = append(env, pin.key+"="+pin.val)
		}
	}
	return env
}

type Sim struct {
	Name  string
	UDID  string
	State string // Booted | Shutdown | ...
}

type simctlDevicesOutput struct {
	Devices map[string][]struct {
		Name  string `json:"name"`
		UDID  string `json:"udid"`
		State string `json:"state"`
	} `json:"devices"`
}

// parseSimctlDevices extracts every simulator regardless of state — the bank
// needs to see shutdown clones, unlike frn's booted-only probe.
func parseSimctlDevices(data []byte) ([]Sim, error) {
	var parsed simctlDevicesOutput
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	var out []Sim
	for _, list := range parsed.Devices {
		for _, d := range list {
			out = append(out, Sim{Name: d.Name, UDID: d.UDID, State: d.State})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func ListSimulators(ctx context.Context) ([]Sim, error) {
	execCtx, cancel := boundedCtx(ctx)
	defer cancel()
	out, err := exec.CommandContext(execCtx, "xcrun", "simctl", "list", "devices", "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("simctl list devices: %w", err)
	}
	return parseSimctlDevices(out)
}

type simctlDeviceTypesOutput struct {
	DeviceTypes []struct {
		Name       string `json:"name"`
		Identifier string `json:"identifier"`
	} `json:"devicetypes"`
}

var iphoneVersion = regexp.MustCompile(`^iPhone (\d+)( .*)?$`)

// pickIPhoneDeviceType returns the identifier of the newest numbered base
// iPhone (highest version, shortest name so "17" beats "17 Pro Max").
// Un-numbered models (iPhone Air, SE) are skipped — version ordering is
// undefined for them.
func pickIPhoneDeviceType(data []byte) (string, error) {
	var parsed simctlDeviceTypesOutput
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", err
	}
	best, bestVersion, bestLen := "", -1, 0
	for _, dt := range parsed.DeviceTypes {
		m := iphoneVersion.FindStringSubmatch(dt.Name)
		if m == nil {
			continue
		}
		v, _ := strconv.Atoi(m[1])
		if v > bestVersion || (v == bestVersion && len(dt.Name) < bestLen) {
			best, bestVersion, bestLen = dt.Identifier, v, len(dt.Name)
		}
	}
	if best == "" {
		return "", fmt.Errorf("no numbered iPhone device type found")
	}
	return best, nil
}

type simctlRuntimesOutput struct {
	Runtimes []struct {
		Identifier  string `json:"identifier"`
		IsAvailable bool   `json:"isAvailable"`
		Platform    string `json:"platform"`
		Version     string `json:"version"`
	} `json:"runtimes"`
}

// pickIOSRuntime returns the identifier of the newest available iOS runtime.
func pickIOSRuntime(data []byte) (string, error) {
	var parsed simctlRuntimesOutput
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", err
	}
	best, bestVersion := "", []int(nil)
	for _, rt := range parsed.Runtimes {
		if !rt.IsAvailable || rt.Platform != "iOS" {
			continue
		}
		v := parseVersion(rt.Version)
		if best == "" || versionLess(bestVersion, v) {
			best, bestVersion = rt.Identifier, v
		}
	}
	if best == "" {
		return "", fmt.Errorf("no available iOS runtime found")
	}
	return best, nil
}

func parseVersion(s string) []int {
	parts := strings.Split(s, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, _ := strconv.Atoi(p)
		out = append(out, n)
	}
	return out
}

func versionLess(a, b []int) bool {
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return x < y
		}
	}
	return false
}

func CreateSimulator(ctx context.Context, name string) (string, error) {
	execCtx, cancel := boundedCtx(ctx)
	defer cancel()
	dtData, err := exec.CommandContext(execCtx, "xcrun", "simctl", "list", "devicetypes", "--json").Output()
	if err != nil {
		return "", fmt.Errorf("simctl list devicetypes: %w", err)
	}
	deviceType, err := pickIPhoneDeviceType(dtData)
	if err != nil {
		return "", err
	}
	rtData, err := exec.CommandContext(execCtx, "xcrun", "simctl", "list", "runtimes", "--json").Output()
	if err != nil {
		return "", fmt.Errorf("simctl list runtimes: %w", err)
	}
	runtime, err := pickIOSRuntime(rtData)
	if err != nil {
		return "", err
	}
	out, err := exec.CommandContext(execCtx, "xcrun", "simctl", "create", name, deviceType, runtime).Output()
	if err != nil {
		return "", fmt.Errorf("simctl create %s: %w", name, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func DeleteSimulator(ctx context.Context, udid string) error {
	execCtx, cancel := boundedCtx(ctx)
	defer cancel()
	if err := exec.CommandContext(execCtx, "xcrun", "simctl", "delete", udid).Run(); err != nil {
		return fmt.Errorf("simctl delete %s: %w", udid, err)
	}
	return nil
}

// BootSimulator boots the simulator (no-op when already booted) and blocks
// until it is usable, under bootTimeout.
func BootSimulator(ctx context.Context, udid string) error {
	bootCtx, cancel := context.WithTimeout(ctx, bootTimeout)
	defer cancel()
	if err := exec.CommandContext(bootCtx, "xcrun", "simctl", "bootstatus", udid, "-b").Run(); err != nil {
		return fmt.Errorf("simctl bootstatus %s: %w", udid, err)
	}
	return nil
}

// ShutdownSimulator shuts the simulator down; already-shutdown is not an error.
func ShutdownSimulator(ctx context.Context, udid string) error {
	execCtx, cancel := boundedCtx(ctx)
	defer cancel()
	out, err := exec.CommandContext(execCtx, "xcrun", "simctl", "shutdown", udid).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "current state: Shutdown") {
		return fmt.Errorf("simctl shutdown %s: %w", udid, err)
	}
	return nil
}

// EraseSimulator wipes the simulator's data. The simulator must be shutdown.
func EraseSimulator(ctx context.Context, udid string) error {
	execCtx, cancel := boundedCtx(ctx)
	defer cancel()
	if err := exec.CommandContext(execCtx, "xcrun", "simctl", "erase", udid).Run(); err != nil {
		return fmt.Errorf("simctl erase %s: %w", udid, err)
	}
	return nil
}

func ListAVDs(ctx context.Context) ([]string, error) {
	emu, err := emulatorPath()
	if err != nil {
		return nil, err
	}
	execCtx, cancel := boundedCtx(ctx)
	defer cancel()
	cmd := exec.CommandContext(execCtx, emu, "-list-avds")
	cmd.Env = androidEnv()
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("emulator -list-avds: %w", err)
	}
	var avds []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		// Recent emulators print an INFO banner line before the list.
		if line == "" || strings.HasPrefix(line, "INFO") {
			continue
		}
		avds = append(avds, line)
	}
	return avds, nil
}

// newestSystemImage walks <sdk>/system-images for complete images (a
// system.img on disk — bare directory shells from aborted sdkmanager runs
// are skipped) and returns the package path of the highest API level.
func newestSystemImage() (string, error) {
	root := filepath.Join(androidSDKRoot(), "system-images")
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", fmt.Errorf("no system images under %s: %w", root, err)
	}
	bestAPI, bestPkg := -1, ""
	for _, apiDir := range entries {
		api := 0
		if _, err := fmt.Sscanf(apiDir.Name(), "android-%d", &api); err != nil {
			continue
		}
		tags, err := os.ReadDir(filepath.Join(root, apiDir.Name()))
		if err != nil {
			continue
		}
		for _, tag := range tags {
			abis, err := os.ReadDir(filepath.Join(root, apiDir.Name(), tag.Name()))
			if err != nil {
				continue
			}
			for _, abi := range abis {
				img := filepath.Join(root, apiDir.Name(), tag.Name(), abi.Name(), "system.img")
				if _, err := os.Stat(img); err != nil {
					continue
				}
				if api > bestAPI {
					bestAPI = api
					bestPkg = strings.Join([]string{"system-images", apiDir.Name(), tag.Name(), abi.Name()}, ";")
				}
			}
		}
	}
	if bestPkg == "" {
		return "", fmt.Errorf("no complete system image under %s — install one via sdkmanager (e.g. \"system-images;android-35;google_apis;arm64-v8a\")", root)
	}
	return bestPkg, nil
}

// fallbackDevice is the profile new AVDs fall back to when the catalog
// exposes no base Pixel. medium_phone is the modern AVD default
// (1080x2400 @ 420dpi) and ships in every catalog — never the tiny
// 320x640 / 160dpi generic avdmanager would otherwise pick on its own.
const fallbackDevice = "medium_phone"

// basePixelID matches a base Pixel id line from `avdmanager list device`
// (lines like `id: 42 or "pixel_8"`). Suffixed variants — pixel_8_pro,
// pixel_8a, pixel_9_pro_fold, pixel_tablet, pixel_xl — are deliberately
// excluded: the base phone is the realistic dev target, mirroring the iOS
// side's "newest base iPhone".
var basePixelID = regexp.MustCompile(`(?m)^\s*id:\s*\d+\s+or\s+"(pixel_(\d+))"\s*$`)

// pickAndroidDevice returns the newest base Pixel id in `avdmanager list
// device` output, or fallbackDevice when none is present. The catalog is
// shipped inside the cmdline-tools package, so the pick advances on its own
// as the SDK tooling is updated — no hardcoded device to go stale.
func pickAndroidDevice(out []byte) string {
	best, bestID := -1, ""
	for _, m := range basePixelID.FindAllStringSubmatch(string(out), -1) {
		n, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		if n > best {
			best, bestID = n, m[1]
		}
	}
	if bestID == "" {
		return fallbackDevice
	}
	return bestID
}

// CreateAVD provisions a new bank AVD on the newest installed system image,
// cut from the newest base Pixel profile in the catalog. Without a -d
// profile avdmanager defaults to a 320x640 / 160dpi generic screen — the
// "tiny emulator" forge runs used to lease.
func CreateAVD(ctx context.Context, name string) error {
	avdmanager, err := avdmanagerPath()
	if err != nil {
		return err
	}
	pkg, err := newestSystemImage()
	if err != nil {
		return err
	}
	execCtx, cancel := context.WithTimeout(ctx, bootTimeout)
	defer cancel()
	listCmd := exec.CommandContext(execCtx, avdmanager, "list", "device")
	listCmd.Env = androidEnv()
	devData, err := listCmd.Output()
	if err != nil {
		return fmt.Errorf("avdmanager list device: %w", err)
	}
	cmd := exec.CommandContext(execCtx, avdmanager, "create", "avd", "-n", name, "-k", pkg, "-d", pickAndroidDevice(devData))
	cmd.Env = androidEnv()
	// avdmanager only prompts for a custom hardware profile when -d is absent;
	// with the device pinned it skips the prompt, but decline defensively.
	cmd.Stdin = strings.NewReader("no\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("avdmanager create avd %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func DeleteAVD(ctx context.Context, name string) error {
	avdmanager, err := avdmanagerPath()
	if err != nil {
		return err
	}
	execCtx, cancel := boundedCtx(ctx)
	defer cancel()
	cmd := exec.CommandContext(execCtx, avdmanager, "delete", "avd", "-n", name)
	cmd.Env = androidEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("avdmanager delete avd %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// StartEmulator launches the AVD detached on its deterministic port. The
// emulator process must outlive the CLI invocation, so it is fully detached
// (own session, stdio to the atelierd log).
func StartEmulator(avd string, port int, wipeData bool) error {
	emu, err := emulatorPath()
	if err != nil {
		return err
	}
	return spawnDetached(androidEnv(), emu, emulatorArgs(avd, port, wipeData)...)
}

// emulatorMemoryMB overrides the hw.ramSize an AVD was provisioned with, on
// every start, so AVDs cut at 2 GB get 4 GB without a bank init: Android
// wedges under load at 2 GB.
const emulatorMemoryMB = 4096

func emulatorArgs(avd string, port int, wipeData bool) []string {
	args := []string{"-avd", avd, "-port", strconv.Itoa(port), "-memory", strconv.Itoa(emulatorMemoryMB), "-no-snapshot", "-no-boot-anim"}
	if wipeData {
		args = append(args, "-wipe-data")
	}
	return args
}

// WaitEmulatorReady blocks until the emulator's Android system is fully
// booted, under bootTimeout.
func WaitEmulatorReady(ctx context.Context, serial string) error {
	waitCtx, cancel := context.WithTimeout(ctx, bootTimeout)
	defer cancel()
	if err := exec.CommandContext(waitCtx, "adb", "-s", serial, "wait-for-device").Run(); err != nil {
		return fmt.Errorf("adb wait-for-device %s: %w", serial, err)
	}
	for {
		out, err := exec.CommandContext(waitCtx, "adb", "-s", serial, "shell", "getprop", "sys.boot_completed").Output()
		if err == nil && strings.TrimSpace(string(out)) == "1" {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("emulator %s did not finish booting: %w", serial, waitCtx.Err())
		case <-time.After(2 * time.Second):
		}
	}
}

// EmulatorBooted reports whether the emulator serial is online and fully
// booted — the cheap health check for an already-running AVD.
func EmulatorBooted(ctx context.Context, serial string) bool {
	execCtx, cancel := boundedCtx(ctx)
	defer cancel()
	out, err := exec.CommandContext(execCtx, "adb", "-s", serial, "shell", "getprop", "sys.boot_completed").Output()
	return err == nil && strings.TrimSpace(string(out)) == "1"
}

// KillEmulator stops the emulator process behind serial; an already-dead
// emulator is not an error.
func KillEmulator(ctx context.Context, serial string) {
	execCtx, cancel := boundedCtx(ctx)
	defer cancel()
	_ = exec.CommandContext(execCtx, "adb", "-s", serial, "emu", "kill").Run()
}

// parseAdbDevices reads `adb devices`: every listed serial counts, offline
// included — an offline emulator still holds its memory.
func parseAdbDevices(out []byte) []string {
	var serials []string
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] == "List" || strings.HasPrefix(fields[0], "*") {
			continue
		}
		serials = append(serials, fields[0])
	}
	return serials
}

func AdbSerials(ctx context.Context) ([]string, error) {
	execCtx, cancel := boundedCtx(ctx)
	defer cancel()
	out, err := exec.CommandContext(execCtx, "adb", "devices").Output()
	if err != nil {
		return nil, fmt.Errorf("adb devices: %w", err)
	}
	return parseAdbDevices(out), nil
}

// StopEmulator kills the emulator and waits until adb no longer lists it:
// `adb emu kill` returns before the process is gone, and the ignition limit
// may only drop once the memory is actually released.
func StopEmulator(ctx context.Context, serial string) error {
	KillEmulator(ctx, serial)
	stopCtx, cancel := boundedCtx(ctx)
	defer cancel()
	for {
		serials, err := AdbSerials(stopCtx)
		if err == nil && !slices.Contains(serials, serial) {
			return nil
		}
		select {
		case <-stopCtx.Done():
			return fmt.Errorf("emulator %s still running after adb emu kill", serial)
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// Notify posts a macOS notification, best-effort. Title and body travel as
// osascript arguments so no quoting reaches AppleScript.
func Notify(title, body string) {
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	_ = exec.CommandContext(ctx, "osascript",
		"-e", "on run argv",
		"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
		"-e", "end run",
		title, body).Run()
}
