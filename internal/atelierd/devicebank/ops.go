package devicebank

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/device"
)

// Sentinel errors the CLI maps to distinct exit codes (10 and 11).
var (
	ErrExhausted      = errors.New("device bank exhausted: every device of the platform is leased or recycling")
	ErrNotInitialized = errors.New("device bank not initialized — run `atelierd device bank init` first")
)

// probePhysical converts frn's physical-device probe into lease candidates
// for one platform. Bank AVDs show up in adb as emulators, not physical, so
// the probe never aliases a bank device.
func probePhysical(ctx context.Context, platform Platform) []PhysicalDevice {
	res := device.ProbeAll(ctx)
	var out []PhysicalDevice
	for _, d := range res.Physical {
		p := PlatformAndroid
		if d.Kind == device.KindIOS {
			p = PlatformIOS
		}
		if p == platform {
			out = append(out, PhysicalDevice{ID: d.ID, Name: d.Label, Platform: p})
		}
	}
	return out
}

// reapLocked runs the lazy lifecycle pass and reports whether it changed
// the state. Callers must hold the flock. TTL-expired leases release their
// devices (recycle for virtual); free virtual devices idle past IdleShutdown
// are powered off; recycling entries stuck past StuckRecycle get their
// worker re-spawned.
func reapLocked(ctx context.Context, s *State, now time.Time) bool {
	changed := false
	for _, d := range reapExpired(s, now) {
		changed = true
		_ = SpawnRecycle(d.Name)
	}
	for _, d := range idleDevices(s, now) {
		if d.Platform == PlatformIOS {
			if err := ShutdownSimulator(ctx, d.UDID); err != nil {
				continue
			}
		} else {
			KillEmulator(ctx, d.TargetID())
		}
		d.State = StateOff
		changed = true
	}
	for _, d := range stuckRecycles(s, now) {
		d.RecycleStartedAt = now
		changed = true
		_ = SpawnRecycle(d.Name)
	}
	return changed
}

// Reap runs the lazy lifecycle pass standalone (status does this so stale
// leases never linger just because nobody called lease/release).
func Reap(ctx context.Context) error {
	return WithLock(func(s *State) error {
		if !reapLocked(ctx, s, time.Now()) {
			return errNoChange
		}
		return nil
	})
}

// ensureBooted boots a cold virtual device and verifies a warm one answers.
// An error means the device is wedged — the caller recycles it and tries
// the next candidate.
func ensureBooted(ctx context.Context, d *Device) error {
	if d.Platform == PlatformIOS {
		return BootSimulator(ctx, d.UDID)
	}
	serial := d.TargetID()
	if EmulatorBooted(ctx, serial) {
		return nil
	}
	KillEmulator(ctx, serial)
	if err := StartEmulator(d.AVD, d.Port, false); err != nil {
		return err
	}
	return WaitEmulatorReady(ctx, serial)
}

// Acquire leases a device of the platform for the session and returns its
// targetable ID. Idempotent per (session, platform). Wedged devices are sent
// to recycling and the next candidate is tried. Returns ErrNotInitialized /
// ErrExhausted for the distinct CLI exit codes.
func Acquire(ctx context.Context, session, workdir string, platform Platform, progress io.Writer) (string, error) {
	physical := probePhysical(ctx, platform)
	for {
		var leased *Lease
		var picked *Device
		// The sentinel outcomes travel outside the WithLock error so the
		// reap pass's mutations persist even when no lease is granted —
		// its side effects (shutdowns, recycle spawns) already happened.
		var outcome error
		err := WithLock(func(s *State) error {
			now := time.Now()
			reaped := reapLocked(ctx, s, now)
			noGrant := func(sentinel error) error {
				outcome = sentinel
				if reaped {
					return nil
				}
				return errNoChange
			}
			if !s.Initialized() {
				return noGrant(ErrNotInitialized)
			}
			if l := acquireExisting(s, session, platform, now); l != nil {
				leased = l
				return nil
			}
			c := pickCandidate(s, platform, physical)
			if c == nil {
				return noGrant(ErrExhausted)
			}
			leased = commitLease(s, c, session, workdir, platform, now)
			picked = c.Device
			return nil
		})
		if err != nil {
			return "", err
		}
		if outcome != nil {
			return "", outcome
		}
		if picked == nil {
			// Existing lease or physical device — nothing to boot.
			return leased.DeviceID, nil
		}
		bootErr := ensureBooted(ctx, picked)
		if bootErr == nil {
			return leased.DeviceID, nil
		}
		fmt.Fprintf(progress, "%s is wedged (%v), recycling and trying the next device\n", picked.Name, bootErr)
		var shouldRecycle bool
		relockErr := WithLock(func(s *State) error {
			now := time.Now()
			// Transition by device identity: the lease can vanish in the
			// unlocked boot window (racing session-end), and an orphaned
			// Leased device has no reap path. Only the call that performs
			// the transition spawns the recycle worker — never two.
			if d := s.FindDevice(picked.Name); d != nil && d.State != StateRecycling {
				d.State = StateRecycling
				d.RecycleStartedAt = now
				d.LastUsedAt = now
				shouldRecycle = true
			}
			if l := s.FindLease(session, platform); l != nil {
				dropLease(s, l, now)
			}
			return nil
		})
		if relockErr != nil {
			return "", relockErr
		}
		if shouldRecycle {
			_ = SpawnRecycle(picked.Name)
		}
	}
}

// Release drops the session's leases (all platforms when platform is empty).
// It returns immediately: virtual devices recycle in a detached worker,
// physical devices are leasable again the moment the lease is gone.
func Release(ctx context.Context, session string, platform Platform) error {
	return WithLock(func(s *State) error {
		now := time.Now()
		reapLocked(ctx, s, now)
		for _, d := range releaseSession(s, session, platform, now) {
			_ = SpawnRecycle(d.Name)
		}
		return nil
	})
}

// OnEmit is the `atelierd emit` hook: renew the session's leases on every
// event, and release them on hook:session-end. Strictly best-effort — a
// lease-state failure must never fail the emit — and a no-op on machines
// without a bank.
func OnEmit(session string, sessionEnd bool) {
	if session == "" || !Exists() {
		return
	}
	_ = WithLock(func(s *State) error {
		now := time.Now()
		// Every lease the release below would drop is renewed first, so a
		// zero touch count means the session holds nothing — no change.
		if touchSession(s, session, now) == 0 {
			return errNoChange
		}
		if sessionEnd {
			for _, d := range releaseSession(s, session, "", now) {
				_ = SpawnRecycle(d.Name)
			}
		}
		return nil
	})
}

// RunRecycle is the detached worker behind `atelierd device recycle`:
// shutdown + erase + boot, then mark the device free. On failure the device
// stays in recycling and the reap pass re-spawns a worker later.
func RunRecycle(ctx context.Context, name string) error {
	s, err := Load()
	if err != nil {
		return err
	}
	d := s.FindDevice(name)
	if d == nil {
		return fmt.Errorf("unknown bank device %q", name)
	}
	if d.Platform == PlatformIOS {
		if err := ShutdownSimulator(ctx, d.UDID); err != nil {
			return err
		}
		if err := EraseSimulator(ctx, d.UDID); err != nil {
			return err
		}
		if err := BootSimulator(ctx, d.UDID); err != nil {
			return err
		}
	} else {
		serial := d.TargetID()
		KillEmulator(ctx, serial)
		if err := StartEmulator(d.AVD, d.Port, true); err != nil {
			return err
		}
		if err := WaitEmulatorReady(ctx, serial); err != nil {
			return err
		}
	}
	return WithLock(func(s *State) error {
		if cur := s.FindDevice(name); cur != nil && cur.State == StateRecycling {
			cur.State = StateFree
			cur.LastUsedAt = time.Now()
			cur.RecycleStartedAt = time.Time{}
		}
		return nil
	})
}

// InitBank provisions the bank to nIOS + nAndroid clones, two-way: missing
// clones are created, free excess clones are deleted, leased excess clones
// survive with a warning. A missing toolchain degrades to provisioning the
// other side with a warning (exit 0). Idempotent.
func InitBank(ctx context.Context, nIOS, nAndroid int, out io.Writer) error {
	hasXcode, hasAndroid := HasXcode(), HasAndroidSDK()
	if !hasXcode {
		fmt.Fprintln(out, "warning: Xcode toolchain not found — skipping the iOS side of the bank")
	}
	if !hasAndroid {
		fmt.Fprintln(out, "warning: Android SDK not found — skipping the Android side of the bank")
	}
	return WithLock(func(s *State) error {
		now := time.Now()
		reapLocked(ctx, s, now)
		s.Config = Config{IOS: nIOS, Android: nAndroid}
		if hasXcode {
			if err := resizeIOS(ctx, s, nIOS, now, out); err != nil {
				return err
			}
		}
		if hasAndroid {
			if err := resizeAndroid(ctx, s, nAndroid, now, out); err != nil {
				return err
			}
		}
		return nil
	})
}

// removeDevice drops the named device from the state.
func removeDevice(s *State, name string) {
	for i, d := range s.Devices {
		if d.Name == name {
			s.Devices = append(s.Devices[:i], s.Devices[i+1:]...)
			return
		}
	}
}

func resizeIOS(ctx context.Context, s *State, n int, now time.Time, out io.Writer) error {
	sims, err := ListSimulators(ctx)
	if err != nil {
		return err
	}
	simByName := map[string]Sim{}
	for _, sim := range sims {
		if bankIndex(PlatformIOS, sim.Name) > 0 {
			simByName[sim.Name] = sim
		}
	}
	// Reconcile with reality before sizing: adopt clones the state lost
	// track of, drop entries whose simulator vanished. simctl allows
	// duplicate names, so re-creating over an orphan would fork the bank.
	for name, sim := range simByName {
		if s.FindDevice(name) == nil {
			st := StateOff
			if sim.State == "Booted" {
				st = StateFree
			}
			s.Devices = append(s.Devices, &Device{
				Name: name, Platform: PlatformIOS, UDID: sim.UDID, State: st, LastUsedAt: now,
			})
		}
	}
	for _, d := range append([]*Device(nil), s.Devices...) {
		if d.Platform == PlatformIOS {
			if _, ok := simByName[d.Name]; !ok {
				removeDevice(s, d.Name)
			}
		}
	}

	plan := planResize(s, PlatformIOS, n)
	for _, name := range plan.Create {
		udid, err := CreateSimulator(ctx, name)
		if err != nil {
			return err
		}
		s.Devices = append(s.Devices, &Device{
			Name: name, Platform: PlatformIOS, UDID: udid, State: StateOff, LastUsedAt: now,
		})
		fmt.Fprintf(out, "created simulator %s (%s)\n", name, udid)
	}
	for _, name := range plan.Delete {
		if err := DeleteSimulator(ctx, s.FindDevice(name).UDID); err != nil {
			return err
		}
		removeDevice(s, name)
		fmt.Fprintf(out, "deleted excess simulator %s\n", name)
	}
	for _, name := range plan.Keep {
		fmt.Fprintf(out, "warning: %s is leased — kept for now, will be removed on a later bank init\n", name)
	}
	return nil
}

func resizeAndroid(ctx context.Context, s *State, n int, now time.Time, out io.Writer) error {
	avds, err := ListAVDs(ctx)
	if err != nil {
		return err
	}
	avdSet := map[string]bool{}
	for _, avd := range avds {
		if bankIndex(PlatformAndroid, avd) > 0 {
			avdSet[avd] = true
		}
	}
	for avd := range avdSet {
		if d := s.FindDevice(avd); d == nil {
			s.Devices = append(s.Devices, &Device{
				Name: avd, Platform: PlatformAndroid, AVD: avd,
				Port: EmulatorPort(bankIndex(PlatformAndroid, avd)), State: StateOff, LastUsedAt: now,
			})
		} else {
			// Re-derive the port on every init so a port-scheme change
			// heals existing entries instead of stranding them on a dead
			// serial.
			d.Port = EmulatorPort(bankIndex(PlatformAndroid, avd))
		}
	}
	for _, d := range append([]*Device(nil), s.Devices...) {
		if d.Platform == PlatformAndroid && !avdSet[d.Name] {
			removeDevice(s, d.Name)
		}
	}

	plan := planResize(s, PlatformAndroid, n)
	for _, name := range plan.Create {
		if err := CreateAVD(ctx, name); err != nil {
			return err
		}
		s.Devices = append(s.Devices, &Device{
			Name: name, Platform: PlatformAndroid, AVD: name,
			Port: EmulatorPort(bankIndex(PlatformAndroid, name)), State: StateOff, LastUsedAt: now,
		})
		fmt.Fprintf(out, "created AVD %s\n", name)
	}
	for _, name := range plan.Delete {
		d := s.FindDevice(name)
		KillEmulator(ctx, d.TargetID())
		if err := DeleteAVD(ctx, name); err != nil {
			return err
		}
		removeDevice(s, name)
		fmt.Fprintf(out, "deleted excess AVD %s\n", name)
	}
	for _, name := range plan.Keep {
		fmt.Fprintf(out, "warning: %s is leased — kept for now, will be removed on a later bank init\n", name)
	}
	return nil
}

// StatusRow is one line of `atelierd device status`: a bank device or a
// connected physical device, with its lease when held.
type StatusRow struct {
	Name     string
	Platform Platform
	Physical bool
	State    DeviceState
	Lease    *Lease
}

// StatusRows builds the status listing: the virtual bank from the state
// snapshot, plus every connected physical device.
func StatusRows(ctx context.Context) ([]StatusRow, error) {
	s, err := Load()
	if err != nil {
		return nil, err
	}
	leaseByName := map[string]*Lease{}
	leaseByID := map[string]*Lease{}
	for _, l := range s.Leases {
		if l.DeviceName != "" {
			leaseByName[l.DeviceName] = l
		}
		leaseByID[l.DeviceID] = l
	}
	var rows []StatusRow
	for _, d := range s.Devices {
		rows = append(rows, StatusRow{
			Name: d.Name, Platform: d.Platform, State: d.State, Lease: leaseByName[d.Name],
		})
	}
	res := device.ProbeAll(ctx)
	for _, d := range res.Physical {
		p := PlatformAndroid
		if d.Kind == device.KindIOS {
			p = PlatformIOS
		}
		row := StatusRow{Name: d.Label, Platform: p, Physical: true, State: StateFree}
		if l := leaseByID[d.ID]; l != nil {
			row.State = StateLeased
			row.Lease = l
		}
		rows = append(rows, row)
	}
	return rows, nil
}
