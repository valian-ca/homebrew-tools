package devicebank

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeDriver struct {
	mu        sync.Mutex
	t         testing.TB
	on        map[string]bool
	maxOnSeen int
	calls     []string
	fail      map[string]int
	probeErr  error
	phys      []PhysicalDevice
	unplugged []PhysicalDevice
	physErr   error
	blindFor  int
	onApps    func()
	onStayOn  func(value string)
	installed map[string]map[string]AppInfo
	stayOnVal map[string]string
	reverses  map[string]int
	spawned   []string
	restores  []string
	runSpawns bool
	notes     []string
	onBoot    func()
}

func newFake() *fakeDriver {
	return &fakeDriver{
		on: map[string]bool{}, fail: map[string]int{}, runSpawns: true,
		installed: map[string]map[string]AppInfo{}, stayOnVal: map[string]string{}, reverses: map[string]int{},
	}
}

// Spawned recycle and restore workers run synchronously.
func useFake(t *testing.T) *fakeDriver {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	f := newFake()
	f.t = t
	prevDrv, prevClock, prevPoll, prevRecheck := drv, clock, queuePoll, unpluggedRecheck
	drv, queuePoll, unpluggedRecheck = f, 5*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { drv, clock, queuePoll, unpluggedRecheck = prevDrv, prevClock, prevPoll, prevRecheck })
	return f
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func useClock(t *testing.T, at time.Time) *fakeClock {
	t.Helper()
	c := &fakeClock{now: at}
	clock = func() time.Time {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.now
	}
	return c
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (f *fakeDriver) call(op, target string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, op+" "+target)
	if f.fail[op] > 0 {
		f.fail[op]--
		return fmt.Errorf("%s %s failed", op, target)
	}
	return nil
}

func (f *fakeDriver) setOn(target string, on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.on[target] = on
	n := 0
	for _, v := range f.on {
		if v {
			n++
		}
	}
	f.maxOnSeen = max(f.maxOnSeen, n)
}

func (f *fakeDriver) isOn(target string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.on[target]
}

func (f *fakeDriver) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (f *fakeDriver) resetCalls() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

func (f *fakeDriver) failIfPhysicalTarget(op string, targets ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range append(f.phys, f.unplugged...) {
		for _, target := range targets {
			if target == p.ID || target == p.Serial || target == p.Name {
				f.t.Errorf("%s targeted the physical device %s (%s)", op, p.Name, target)
			}
		}
	}
}

func (f *fakeDriver) bootIOS(_ context.Context, udid string) error {
	f.failIfPhysicalTarget("bootIOS", udid)
	if f.onBoot != nil {
		f.onBoot()
	}
	if err := f.call("bootIOS", udid); err != nil {
		return err
	}
	f.setOn(udid, true)
	return nil
}

func (f *fakeDriver) shutdownIOS(_ context.Context, udid string) error {
	f.failIfPhysicalTarget("shutdownIOS", udid)
	if err := f.call("shutdownIOS", udid); err != nil {
		return err
	}
	f.setOn(udid, false)
	return nil
}

func (f *fakeDriver) eraseIOS(_ context.Context, udid string) error {
	f.failIfPhysicalTarget("eraseIOS", udid)
	return f.call("eraseIOS", udid)
}

func (f *fakeDriver) avdBooted(_ context.Context, serial string) bool { return f.isOn(serial) }

func (f *fakeDriver) startAVD(avd string, port int, wipe bool) error {
	f.failIfPhysicalTarget("startAVD", avd, fmt.Sprintf("emulator-%d", port))
	if err := f.call("startAVD", fmt.Sprintf("%s wipe=%v", avd, wipe)); err != nil {
		return err
	}
	f.setOn(fmt.Sprintf("emulator-%d", port), true)
	return nil
}

func (f *fakeDriver) waitAVDReady(_ context.Context, serial string) error {
	f.failIfPhysicalTarget("waitAVDReady", serial)
	if f.onBoot != nil {
		f.onBoot()
	}
	return f.call("waitAVDReady", serial)
}

func (f *fakeDriver) killAVD(_ context.Context, serial string) error {
	f.failIfPhysicalTarget("killAVD", serial)
	if err := f.call("killAVD", serial); err != nil {
		return err
	}
	f.setOn(serial, false)
	return nil
}

func (f *fakeDriver) poweredOn(context.Context, bool, bool) (map[string]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.probeErr != nil {
		return nil, f.probeErr
	}
	out := map[string]bool{}
	for k, v := range f.on {
		if v {
			out[k] = true
		}
	}
	return out, nil
}

func (f *fakeDriver) physical(_ context.Context, platform Platform) ([]PhysicalDevice, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.physErr != nil {
		return nil, f.physErr
	}
	if f.blindFor > 0 {
		f.blindFor--
		return nil, nil
	}
	var out []PhysicalDevice
	for _, p := range f.phys {
		if platform == "" || p.Platform == platform {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeDriver) plug(devices ...PhysicalDevice) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unplugged = append(f.unplugged, f.phys...)
	f.phys = devices
}

func (f *fakeDriver) serialOf(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.phys {
		if p.ID == id || p.Serial == id {
			return p.Serial
		}
	}
	return id
}

func (f *fakeDriver) install(serial, appID string, app AppInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.installed[serial] == nil {
		f.installed[serial] = map[string]AppInfo{}
	}
	app.Installed = true
	f.installed[serial][appID] = app
}

func (f *fakeDriver) hasApp(serial, appID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.installed[serial][appID]
	return ok
}

func (f *fakeDriver) stayOnOf(serial string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.stayOnVal[serial]; ok {
		return v
	}
	return "null"
}

func (f *fakeDriver) stayOn(_ context.Context, id string) (string, error) {
	if err := f.call("stayOn", id); err != nil {
		return "", err
	}
	return f.stayOnOf(f.serialOf(id)), nil
}

func (f *fakeDriver) setStayOn(_ context.Context, id, value string) error {
	if f.onStayOn != nil {
		hook := f.onStayOn
		f.onStayOn = nil
		hook(value)
	}
	if err := f.call("setStayOn", id+" "+value); err != nil {
		return err
	}
	serial := f.serialOf(id)
	f.mu.Lock()
	defer f.mu.Unlock()
	if value == "null" {
		delete(f.stayOnVal, serial)
	} else {
		f.stayOnVal[serial] = value
	}
	return nil
}

func (f *fakeDriver) removeReverses(_ context.Context, id string) error {
	if err := f.call("removeReverses", id); err != nil {
		return err
	}
	serial := f.serialOf(id)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reverses[serial] = 0
	return nil
}

func (f *fakeDriver) apps(_ context.Context, p PhysicalDevice) ([]string, error) {
	if f.onApps != nil {
		hook := f.onApps
		f.onApps = nil
		hook()
	}
	if err := f.call("apps", p.ID); err != nil {
		return nil, err
	}
	serial := f.serialOf(p.ID)
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for id := range f.installed[serial] {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func (f *fakeDriver) appInfo(_ context.Context, p PhysicalDevice, appID string) (AppInfo, error) {
	if err := f.call("appInfo", p.ID+" "+appID); err != nil {
		return AppInfo{}, err
	}
	serial := f.serialOf(p.ID)
	f.mu.Lock()
	defer f.mu.Unlock()
	app := f.installed[serial][appID]
	if p.Platform == PlatformAndroid {
		app.Name = ""
	}
	return app, nil
}

func (f *fakeDriver) androidLabel(_ context.Context, id, pkg string) (string, error) {
	if err := f.call("androidLabel", id+" "+pkg); err != nil {
		return "", err
	}
	serial := f.serialOf(id)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.installed[serial][pkg].Name, nil
}

func (f *fakeDriver) uninstall(_ context.Context, p PhysicalDevice, appID string) error {
	if err := f.call("uninstall", p.ID+" "+appID); err != nil {
		return err
	}
	serial := f.serialOf(p.ID)
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.installed[serial], appID)
	return nil
}

func (f *fakeDriver) spawnRestore(deviceID string) error {
	f.mu.Lock()
	f.restores = append(f.restores, deviceID)
	run := f.runSpawns
	f.mu.Unlock()
	if run {
		return RunRestore(context.Background(), deviceID)
	}
	return nil
}

func (f *fakeDriver) spawnRecycle(name string) error {
	f.failIfPhysicalTarget("spawnRecycle", name)
	f.mu.Lock()
	f.spawned = append(f.spawned, name)
	run := f.runSpawns
	f.mu.Unlock()
	if run {
		return RunRecycle(context.Background(), name)
	}
	return nil
}

func (f *fakeDriver) notify(title, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notes = append(f.notes, title+": "+body)
}

var t0 = time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)

func iosDevice(i int) *Device {
	return &Device{Name: fmt.Sprintf("atelier-ios-%d", i), Platform: PlatformIOS, UDID: fmt.Sprintf("UDID-%d", i), State: StateOff, LastUsedAt: t0}
}

func androidDevice(i int) *Device {
	return &Device{Name: fmt.Sprintf("atelier-android-%d", i), Platform: PlatformAndroid, AVD: fmt.Sprintf("atelier-android-%d", i), Port: EmulatorPort(i), State: StateOff, LastUsedAt: t0}
}

func bank(devices ...*Device) *State {
	s := &State{Devices: devices}
	for _, d := range devices {
		if d.Platform == PlatformIOS {
			s.Config.IOS++
		} else {
			s.Config.Android++
		}
	}
	return s
}

func seed(t *testing.T, s *State) {
	t.Helper()
	if err := WithLock(func(cur *State) error {
		*cur = *s
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func load(t *testing.T) *State {
	t.Helper()
	s, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func lease(t *testing.T, session string, platform Platform) string {
	t.Helper()
	id, err := Acquire(context.Background(), LeaseRequest{Session: session, Workdir: "/wd", Platform: platform}, discard{})
	if err != nil {
		t.Fatalf("lease %s %s: %v", session, platform, err)
	}
	return id
}

func leaseErr(session string, platform Platform) error {
	_, err := Acquire(context.Background(), LeaseRequest{Session: session, Workdir: "/wd", Platform: platform}, discard{})
	return err
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
