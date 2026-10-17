package devicebank

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/device"
)

// Sentinel errors the CLI maps to distinct exit codes (10 and 11).
var (
	ErrExhausted      = errors.New("no device of the platform could be powered on within the wait: every device is leased, recycling or broken, or the ignition limit is reached")
	ErrNotInitialized = errors.New("device bank not initialized — run `atelierd device bank init` first")
)

// reapLocked runs the lifecycle pass and reports whether it changed the
// state. Callers must hold the flock. TTL-expired leases recycle their
// device, a recycle past its bound counts as a failure, and the recorded
// power state is confronted with reality.
func reapLocked(ctx context.Context, s *State, now time.Time) bool {
	changed := reapExpired(s, now)
	changed = reapStuckRecycles(s, now) || changed
	return reconcile(ctx, s, now) || changed
}

// reconcile corrects the recorded power state against one probe of the
// toolchains. A probe failure leaves every recorded state as it is: no
// state flips on missing data.
func reconcile(ctx context.Context, s *State, now time.Time) bool {
	var ios, android bool
	for _, d := range s.Devices {
		if d.Platform == PlatformIOS {
			ios = true
		} else {
			android = true
		}
	}
	if !ios && !android {
		return false
	}
	real, err := drv.poweredOn(ctx, ios, android)
	probed := err == nil
	changed := false
	for _, d := range s.Devices {
		l := s.leaseForDevice(d.Name)
		reallyOn := probed && real[d.TargetID()]
		switch d.State {
		case StateOn:
			if l == nil {
				if powerOff(ctx, d) == nil {
					markOff(d)
					s.journal(d, StateOn, nil, "reap-unleased-on")
					changed = true
				}
				continue
			}
			// A lease boots its device outside the lock: until the boot
			// returns, the device may not be visible to the toolchain yet.
			booting := d.Booting && now.Sub(d.PoweredOnAt) < bootTimeout
			if probed && !reallyOn && !booting {
				markOff(d)
				l.DeviceDied = true
				s.journal(d, StateOn, l, "died")
				changed = true
			}
		case StateOff, StateBroken:
			if reallyOn && powerOff(ctx, d) == nil {
				reason := "reap-unleased-on"
				if l != nil {
					reason = "reap-off"
				}
				s.journal(d, StateOn, l, reason)
				changed = true
			}
		}
	}
	return changed
}

func markOff(d *Device) {
	d.State = StateOff
	d.PoweredOnAt = time.Time{}
	d.Booting = false
}

// Reap runs the lifecycle pass standalone (status does this so stale
// leases and dead devices never linger just because nobody leased).
func Reap(ctx context.Context) error {
	return WithLock(func(s *State) error {
		now := clock()
		changed := reapLocked(ctx, s, now)
		changed = dropExpiredWaiters(s, now) || changed
		if !changed {
			return errNoChange
		}
		return nil
	})
}

type grant struct {
	lease  *Lease
	boot   *Device
	wipe   bool
	notify *Waiter
}

// grantLocked decides one lease attempt under the lock. A nil lease means
// the caller waits in the queue.
func grantLocked(s *State, session, workdir string, platform Platform, physical []PhysicalDevice, now, deadline time.Time) (grant, bool) {
	var g grant
	if l := s.FindLease(session, platform); l != nil {
		if l.Physical {
			g.lease = l
			return g, false
		}
		switch d := s.FindDevice(l.DeviceName); {
		case d != nil && d.State == StateOn:
			g.lease = l
			return g, removeWaiters(s, session, platform)
		case d != nil && d.State == StateOff:
			if fairReservation(s, session, platform) == d {
				powerUp(s, d, l, now, "resume")
				removeWaiters(s, session, platform)
				g.lease, g.boot, g.wipe = l, d, d.WipePending
				return g, true
			}
			return queueCaller(s, session, platform, now, deadline)
		default:
			removeLease(s, l)
		}
	}
	if d := fairReservation(s, session, platform); d != nil {
		g.lease = commitLease(s, &Candidate{Device: d}, session, workdir, platform, now)
		removeWaiters(s, session, platform)
		g.boot, g.wipe = d, d.WipePending
		return g, true
	}
	// Physical fallback only when the platform has no virtual device to
	// power on at all: a limit-blocked caller waits for the virtual one.
	if !hasVirtualCandidate(s, platform) && !waiterAhead(s, session, platform) {
		if p := pickPhysical(s, platform, physical); p != nil {
			g.lease = commitLease(s, &Candidate{Physical: p}, session, workdir, platform, now)
			removeWaiters(s, session, platform)
			return g, true
		}
	}
	return queueCaller(s, session, platform, now, deadline)
}

func queueCaller(s *State, session string, platform Platform, now, deadline time.Time) (grant, bool) {
	var g grant
	w, changed := upsertWaiter(s, session, platform, now, deadline)
	if w.NotifiedAt.IsZero() && now.Sub(w.EnqueuedAt) >= NotifyAfter {
		w.NotifiedAt = now
		g.notify = w
		changed = true
	}
	return g, changed
}

// Acquire leases a device of the platform for the session and returns its
// targetable ID. Idempotent per (session, platform); a suspended or dead
// lease gets its own device back, powered on. Within the ignition limit
// and the queue order only; otherwise it waits in the queue up to wait,
// then returns ErrExhausted.
func Acquire(ctx context.Context, session, workdir string, platform Platform, wait time.Duration, progress io.Writer) (string, error) {
	deadline := clock().Add(wait)
	physical := drv.physical(ctx, platform)
	for {
		var g grant
		var outcome error
		err := WithLock(func(s *State) error {
			now := clock()
			changed := reapLocked(ctx, s, now)
			changed = touchSession(s, session, now) > 0 || changed
			changed = dropExpiredWaiters(s, now) || changed
			if !s.Initialized() {
				outcome = ErrNotInitialized
			} else {
				var granted bool
				g, granted = grantLocked(s, session, workdir, platform, physical, now, deadline)
				changed = granted || changed
				if g.lease == nil {
					outcome = ErrExhausted
				}
			}
			if !changed {
				return errNoChange
			}
			return nil
		})
		if err != nil {
			return "", err
		}
		if g.notify != nil {
			drv.notify("atelierd device bank",
				fmt.Sprintf("Session %s has been waiting for an %s device since %s", g.notify.SessionID, g.notify.Platform, g.notify.EnqueuedAt.Local().Format("15:04")))
		}
		if errors.Is(outcome, ErrExhausted) {
			if remaining := deadline.Sub(clock()); remaining > 0 {
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				case <-time.After(min(queuePoll, remaining)):
				}
				continue
			}
		}
		if outcome != nil {
			return "", outcome
		}
		if g.boot == nil {
			return g.lease.DeviceID, nil
		}
		bootErr := boot(ctx, g.boot, g.wipe)
		if err := settleBoot(session, platform, g.boot.Name, g.wipe, bootErr); err != nil {
			return "", err
		}
		if bootErr == nil {
			return g.lease.DeviceID, nil
		}
		fmt.Fprintf(progress, "%s failed to boot (%v), recycling it and trying the next device\n", g.boot.Name, bootErr)
	}
}

// settleBoot records a boot outcome by device identity: the lease can vanish
// in the unlocked boot window (racing session-end).
func settleBoot(session string, platform Platform, name string, wiped bool, bootErr error) error {
	return WithLock(func(s *State) error {
		d := s.FindDevice(name)
		if d == nil || d.State != StateOn {
			return errNoChange
		}
		if bootErr == nil {
			d.Booting = false
			d.RecycleFailures = 0
			if wiped {
				d.WipePending = false
			}
			return nil
		}
		if l := s.FindLease(session, platform); l != nil && l.DeviceName == name {
			removeLease(s, l)
		}
		recordFailure(s, d, firstLine(bootErr), "boot-failed", clock())
		return nil
	})
}

func firstLine(err error) string {
	msg, _, _ := strings.Cut(err.Error(), "\n")
	return msg
}

// Pause suspends every virtual lease of the session: the device is powered
// off, keeps its data, and leaves the ignition limit; the session's next
// lease powers it back on. Physical leases are left as they are.
func Pause(ctx context.Context, session string) error {
	var failures []error
	err := WithLock(func(s *State) error {
		now := clock()
		changed := reapLocked(ctx, s, now)
		changed = touchSession(s, session, now) > 0 || changed
		for _, l := range s.Leases {
			if l.SessionID != session || l.Physical || l.Suspended() {
				continue
			}
			d := s.FindDevice(l.DeviceName)
			if d == nil || (d.State != StateOn && d.State != StateOff) {
				continue
			}
			from := d.State
			if from == StateOn {
				if err := powerOff(ctx, d); err != nil {
					failures = append(failures, fmt.Errorf("pause %s: %w", d.Name, err))
					continue
				}
				markOff(d)
			}
			l.SuspendedAt = now
			l.DeviceDied = false
			s.journal(d, from, l, "pause")
			changed = true
		}
		if !changed {
			return errNoChange
		}
		return nil
	})
	if err != nil {
		return err
	}
	return errors.Join(failures...)
}

// Release drops the session's leases and queue entries (all platforms when
// platform is empty). It returns immediately: virtual devices recycle in a
// detached worker, physical devices are leasable again the moment the lease
// is gone.
func Release(ctx context.Context, session string, platform Platform) error {
	return WithLock(func(s *State) error {
		now := clock()
		changed := reapLocked(ctx, s, now)
		changed = touchSession(s, session, now) > 0 || changed
		changed = releaseSession(s, session, platform, now, "release") || changed
		if !changed {
			return errNoChange
		}
		return nil
	})
}

// OnEmit is the `atelierd emit` hook: renew the session's leases on every
// event, and release them on hook:session-end. Strictly best-effort — a
// lease-state failure must never fail the emit — and a no-op on machines
// without a bank. No reap: it runs on every event.
func OnEmit(session string, sessionEnd bool) {
	if session == "" || !Exists() {
		return
	}
	_ = WithLock(func(s *State) error {
		now := clock()
		changed := touchSession(s, session, now) > 0
		if sessionEnd {
			changed = releaseSession(s, session, "", now, "session-end") || changed
		}
		if !changed {
			return errNoChange
		}
		return nil
	})
}

// RunRecycle is the detached worker behind `atelierd device recycle`:
// confirmed power-off, then erase without booting, then off. An Android
// erase is the -wipe-data of its next boot. The whole recycle is bounded
// by RecycleBound; a failure counts toward broken.
func RunRecycle(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, RecycleBound)
	defer cancel()
	s, err := Load()
	if err != nil {
		return err
	}
	d := s.FindDevice(name)
	if d == nil {
		return fmt.Errorf("unknown bank device %q", name)
	}
	if d.State != StateRecycling {
		return nil
	}
	recycleErr := recycle(ctx, d)
	lockErr := WithLock(func(s *State) error {
		cur := s.FindDevice(name)
		if cur == nil || cur.State != StateRecycling {
			return errNoChange
		}
		now := clock()
		if recycleErr != nil {
			recordFailure(s, cur, firstLine(recycleErr), "recycle-failed", now)
			return nil
		}
		markOff(cur)
		cur.RecycleStartedAt = time.Time{}
		cur.RecycleOff = false
		cur.LastUsedAt = now
		cur.WipePending = cur.Platform == PlatformAndroid
		s.journal(cur, StateRecycling, nil, "recycle-done")
		return nil
	})
	if lockErr != nil {
		return lockErr
	}
	return recycleErr
}

func recycle(ctx context.Context, d *Device) error {
	if err := powerOff(ctx, d); err != nil {
		return err
	}
	err := WithLock(func(s *State) error {
		cur := s.FindDevice(d.Name)
		if cur == nil || cur.State != StateRecycling || cur.RecycleOff {
			return errNoChange
		}
		cur.RecycleOff = true
		s.journal(cur, StateRecycling, nil, "recycle-off")
		return nil
	})
	if err != nil {
		return err
	}
	if d.Platform == PlatformIOS {
		return drv.eraseIOS(ctx, d.UDID)
	}
	return nil
}

func MaxOn() (int, error) {
	s, err := Load()
	if err != nil {
		return 0, err
	}
	return s.maxOn(), nil
}

func SetMaxOn(n int) error {
	if n < 1 {
		return fmt.Errorf("ignition limit must be at least 1, got %d", n)
	}
	return WithLock(func(s *State) error {
		if s.Config.MaxOn == n {
			return errNoChange
		}
		s.Config.MaxOn = n
		return nil
	})
}

// InitBank provisions the bank to nIOS + nAndroid clones, two-way: missing
// clones are created, free excess clones are deleted, leased excess clones
// survive with a warning. Broken devices are repaired. A missing toolchain
// degrades to provisioning the other side with a warning (exit 0).
// Idempotent.
func InitBank(ctx context.Context, nIOS, nAndroid int, out io.Writer) error {
	hasXcode, hasAndroid := HasXcode(), HasAndroidSDK()
	if !hasXcode {
		fmt.Fprintln(out, "warning: Xcode toolchain not found — skipping the iOS side of the bank")
	}
	if !hasAndroid {
		fmt.Fprintln(out, "warning: Android SDK not found — skipping the Android side of the bank")
	}
	// The cap only matters when the Android side will actually provision —
	// a missing SDK keeps the documented degrade-with-warning, exit-0 path.
	if hasAndroid && nAndroid > MaxAndroidBank {
		return fmt.Errorf("android bank size %d exceeds the maximum of %d (adb discovers console ports 5554-5584 only)", nAndroid, MaxAndroidBank)
	}
	return WithLock(func(s *State) error {
		now := clock()
		reapLocked(ctx, s, now)
		s.Config = Config{IOS: nIOS, Android: nAndroid, MaxOn: s.Config.MaxOn}
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

func removeDevice(s *State, name string) {
	for i, d := range s.Devices {
		if d.Name == name {
			s.Devices = append(s.Devices[:i], s.Devices[i+1:]...)
			return
		}
	}
}

func repair(s *State, d *Device, out io.Writer) {
	if d.State != StateBroken {
		return
	}
	markOff(d)
	d.RecycleFailures = 0
	d.BrokenReason = ""
	d.RecycleStartedAt = time.Time{}
	d.RecycleOff = false
	s.journal(d, StateBroken, nil, "repair")
	fmt.Fprintf(out, "repaired broken %s\n", d.Name)
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
				st = StateOn
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
	for _, d := range s.Devices {
		if d.Platform == PlatformIOS {
			repair(s, d, out)
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

	// Re-cut the surviving in-range AVDs so a device-profile change reaches
	// them too: avdmanager bakes the profile at create time, so an existing
	// AVD keeps its old (possibly 320x640) screen until torn down. Leased or
	// recycling AVDs can't be recreated under a live forge — they keep their
	// current profile and warn. Computed before the Create loop so freshly
	// created AVDs (already on the target profile) are not re-cut.
	var recreate []*Device
	for _, d := range s.Devices {
		if d.Platform != PlatformAndroid {
			continue
		}
		idx := bankIndex(PlatformAndroid, d.Name)
		if idx < 1 || idx > n {
			continue
		}
		if s.leaseForDevice(d.Name) != nil || d.State == StateRecycling {
			fmt.Fprintf(out, "warning: %s is leased — kept on its current profile, will be recreated on a later bank init\n", d.Name)
			continue
		}
		recreate = append(recreate, d)
	}

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
	for _, d := range recreate {
		KillEmulator(ctx, d.TargetID())
		if err := DeleteAVD(ctx, d.Name); err != nil {
			return err
		}
		if err := CreateAVD(ctx, d.Name); err != nil {
			return err
		}
		repair(s, d, out)
		markOff(d)
		d.WipePending = false
		d.LastUsedAt = now
		fmt.Fprintf(out, "recreated AVD %s\n", d.Name)
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
// connected physical device, with its lease when held. Device is nil for a
// physical row.
type StatusRow struct {
	Name     string
	Platform Platform
	Physical bool
	State    DeviceState
	Device   *Device
	Lease    *Lease
}

// StatusRows builds the status listing — the virtual bank from the state
// snapshot plus every connected physical device — and the live queue in
// arrival order.
func StatusRows(ctx context.Context) ([]StatusRow, []*Waiter, error) {
	s, err := Load()
	if err != nil {
		return nil, nil, err
	}
	leaseByID := map[string]*Lease{}
	for _, l := range s.Leases {
		leaseByID[l.DeviceID] = l
	}
	var rows []StatusRow
	for _, d := range s.Devices {
		rows = append(rows, StatusRow{
			Name: d.Name, Platform: d.Platform, State: d.State, Device: d, Lease: s.leaseForDevice(d.Name),
		})
	}
	res := device.ProbeAll(ctx)
	for _, d := range res.Physical {
		p := PlatformAndroid
		if d.Kind == device.KindIOS {
			p = PlatformIOS
		}
		row := StatusRow{Name: d.Label, Platform: p, Physical: true, State: StateOff}
		if l := leaseByID[d.ID]; l != nil {
			row.State = StateOn
			row.Lease = l
		}
		rows = append(rows, row)
	}
	return rows, s.liveWaiters(clock(), ""), nil
}
