package devicebank

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeDriver struct {
	mu        sync.Mutex
	on        map[string]bool
	maxOnSeen int
	calls     []string
	fail      map[string]int
	probeErr  error
	phys      []PhysicalDevice
	spawned   []string
	runSpawns bool
	notes     []string
}

func newFake() *fakeDriver {
	return &fakeDriver{on: map[string]bool{}, fail: map[string]int{}, runSpawns: true}
}

// useFake isolates a test: temp HOME for the state file, the fake driver,
// and a fast queue poll. Spawned recycle workers run synchronously.
func useFake(t *testing.T) *fakeDriver {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	f := newFake()
	prevDrv, prevClock, prevPoll := drv, clock, queuePoll
	drv, queuePoll = f, 5*time.Millisecond
	t.Cleanup(func() { drv, clock, queuePoll = prevDrv, prevClock, prevPoll })
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

// count returns how many recorded calls start with prefix.
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

func (f *fakeDriver) bootIOS(_ context.Context, udid string) error {
	if err := f.call("bootIOS", udid); err != nil {
		return err
	}
	f.setOn(udid, true)
	return nil
}

func (f *fakeDriver) shutdownIOS(_ context.Context, udid string) error {
	if err := f.call("shutdownIOS", udid); err != nil {
		return err
	}
	f.setOn(udid, false)
	return nil
}

func (f *fakeDriver) eraseIOS(_ context.Context, udid string) error {
	return f.call("eraseIOS", udid)
}

func (f *fakeDriver) avdBooted(_ context.Context, serial string) bool { return f.isOn(serial) }

func (f *fakeDriver) startAVD(avd string, port int, wipe bool) error {
	if err := f.call("startAVD", fmt.Sprintf("%s wipe=%v", avd, wipe)); err != nil {
		return err
	}
	f.setOn(fmt.Sprintf("emulator-%d", port), true)
	return nil
}

func (f *fakeDriver) waitAVDReady(_ context.Context, serial string) error {
	return f.call("waitAVDReady", serial)
}

func (f *fakeDriver) killAVD(_ context.Context, serial string) error {
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

func (f *fakeDriver) physical(_ context.Context, platform Platform) []PhysicalDevice {
	var out []PhysicalDevice
	for _, p := range f.phys {
		if p.Platform == platform {
			out = append(out, p)
		}
	}
	return out
}

func (f *fakeDriver) spawnRecycle(name string) error {
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
	id, err := Acquire(context.Background(), session, "/wd", platform, 0, discard{})
	if err != nil {
		t.Fatalf("lease %s %s: %v", session, platform, err)
	}
	return id
}

func leaseErr(session string, platform Platform) error {
	_, err := Acquire(context.Background(), session, "/wd", platform, 0, discard{})
	return err
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
