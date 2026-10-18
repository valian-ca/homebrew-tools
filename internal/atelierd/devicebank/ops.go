package devicebank

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// Sentinel errors the CLI maps to distinct exit codes (10 and 11).
var (
	ErrExhausted      = errors.New("no device of the platform could be leased within the wait: every device is leased, recycling or broken, the ignition limit is reached, or no physical device is plugged, free and available")
	ErrNotInitialized = errors.New("device bank not initialized — run `atelierd device bank init` first")
	ErrLeaseLost      = errors.New("the lease was released while its device was being prepared")
)

func reapLocked(ctx context.Context, s *State, now time.Time) bool {
	changed := reapExpired(s, now)
	changed = reapStuckRecycles(s, now) || changed
	changed = reapRestores(ctx, s, now) || changed
	return reconcile(ctx, s, now) || changed
}

// powerOffRetryAfter spaces out power-off attempts on a device that refused
// one: each attempt can hold the devices lock for its whole timeout, and
// every lease poll reaps.
const powerOffRetryAfter = 5 * time.Minute

// reapPowerOff attempts a reconcile power-off and records a refusal, so the
// state is saved either way.
func reapPowerOff(ctx context.Context, d *Device, now time.Time) (off, changed bool) {
	if !d.PowerOffFailedAt.IsZero() && now.Sub(d.PowerOffFailedAt) < powerOffRetryAfter {
		return false, false
	}
	if err := powerOff(ctx, d); err != nil {
		d.PowerOffFailedAt = now
		return false, true
	}
	d.PowerOffFailedAt = time.Time{}
	return true, true
}

// reconcile confronts the recorded power state with one probe of the
// toolchains. A failed probe never marks a device dead or found on; an
// unleased on device is powered off from the record alone.
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
				off, attempted := reapPowerOff(ctx, d, now)
				changed = attempted || changed
				if off {
					markOff(d)
					s.journal(d, StateOn, nil, "reap-unleased-on")
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
			if !reallyOn {
				if probed && d.State == StateBroken && !d.PowerOffConfirmed {
					d.PowerOffConfirmed = true
					changed = true
				}
				continue
			}
			off, attempted := reapPowerOff(ctx, d, now)
			changed = attempted || changed
			if !off {
				continue
			}
			reason := "reap-unleased-on"
			if l != nil {
				reason = "reap-off"
			}
			if d.State == StateBroken {
				d.PowerOffConfirmed = true
			}
			s.journal(d, StateOn, l, reason)
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
	lease        *Lease
	boot         *Device
	wipe         bool
	notify       *Waiter
	take         *PhysicalDevice
	resumeStayOn bool
	notes        []string
}

// takeWipe consumes a pending wipe when its boot is granted: once the
// emulator starts with -wipe-data the erase has happened, even if this
// process dies before the boot returns, and a later resume must not wipe
// again. A failed boot goes back through recycling, which sets it again.
func takeWipe(d *Device) bool {
	wipe := d.WipePending
	d.WipePending = false
	return wipe
}

// grantLocked takes the whole decision under the lock: a physical device on
// the cable when the requirement admits one, else a virtual device through
// the ignition limit and the queue. A physical device never joins the queue,
// and a requirement without physical never returns one.
func grantLocked(s *State, req LeaseRequest, probe physicalProbe, excluded map[string]string, now, deadline time.Time) (g grant, changed bool) {
	session, platform := req.Session, req.Platform
	if l := s.FindLease(session, platform); l != nil {
		if l.Physical {
			wasSuspended := l.Suspended()
			if kept, resume := holdPhysical(l, probe); kept {
				g.lease, g.resumeStayOn = l, resume
				return g, removeWaiters(s, session, platform) || wasSuspended != l.Suspended()
			}
			g.notes = append(g.notes, fmt.Sprintf("note: %s (%s) is no longer connected by cable — lease dropped, choosing again", l.DeviceName, l.serial()))
			dropLease(s, l, now, "unplugged")
			changed = true
		} else {
			switch d := s.FindDevice(l.DeviceName); {
			case d != nil && d.State == StateOn:
				g.lease = l
				return g, removeWaiters(s, session, platform)
			case d != nil && d.State == StateOff:
				if fairReservation(s, session, platform) == d {
					powerUp(s, d, l, now, "resume")
					removeWaiters(s, session, platform)
					g.lease, g.boot, g.wipe = l, d, takeWipe(d)
					return g, true
				}
				return queueCaller(s, session, platform, now, deadline)
			default:
				removeLease(s, l)
				changed = true
			}
		}
	}
	if req.Type.admitsPhysical() {
		if req.AppID == "" {
			g.notes = append(g.notes, "note: no --app-id given — physical devices are not leased")
		} else if p := pickPhysical(s, platform, probe.devices, excluded, &g.notes); p != nil {
			l := commitLease(s, &Candidate{Physical: p}, session, req.Workdir, platform, now)
			l.AppID = req.AppID
			removeWaiters(s, session, platform)
			g.lease, g.take = l, p
			return g, true
		}
	}
	if !req.Type.admitsVirtual() {
		return g, changed
	}
	if d := fairReservation(s, session, platform); d != nil {
		g.lease = commitLease(s, &Candidate{Device: d}, session, req.Workdir, platform, now)
		removeWaiters(s, session, platform)
		g.boot, g.wipe = d, takeWipe(d)
		return g, true
	}
	q, queued := queueCaller(s, session, platform, now, deadline)
	q.notes = g.notes
	return q, queued || changed
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

// Acquire is idempotent per (session, platform): a suspended or dead lease
// gets its own device back, powered on. Physical devices are probed again on
// every attempt, so one plugged or freed during the wait is taken.
func Acquire(ctx context.Context, req LeaseRequest, progress io.Writer) (string, error) {
	if req.Type == "" {
		req.Type = RequireVirtual
	}
	session, platform := req.Session, req.Platform
	deadline := clock().Add(req.Wait)
	excluded := map[string]string{}
	printed := map[string]bool{}
	note := func(lines []string) {
		for _, line := range lines {
			if !printed[line] {
				printed[line] = true
				fmt.Fprintln(progress, line)
			}
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			leaveQueue(session, platform)
			return "", err
		}
		var probe physicalProbe
		if req.Type.admitsPhysical() && req.AppID != "" {
			devices, err := drv.physical(ctx, platform)
			probe = physicalProbe{ok: err == nil, devices: devices}
		}
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
				g, granted = grantLocked(s, req, probe, excluded, now, deadline)
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
		note(g.notes)
		if g.notify != nil {
			drv.notify("atelierd device bank",
				fmt.Sprintf("Session %s has been waiting for an %s device since %s", g.notify.SessionID, g.notify.Platform, g.notify.EnqueuedAt.Local().Format("15:04")))
		}
		if errors.Is(outcome, ErrExhausted) {
			if remaining := deadline.Sub(clock()); remaining > 0 {
				select {
				case <-ctx.Done():
					leaveQueue(session, platform)
					return "", ctx.Err()
				case <-time.After(min(queuePoll, remaining)):
				}
				continue
			}
		}
		if outcome != nil {
			return "", outcome
		}
		if g.take != nil {
			skip, err := takePhysical(ctx, req, g.lease, *g.take)
			if err != nil {
				return "", err
			}
			if skip != "" {
				excluded[g.take.Serial] = skip
				note([]string{skip})
				continue
			}
			return g.lease.DeviceID, nil
		}
		if g.resumeStayOn {
			if err := drv.setStayOn(ctx, g.lease.DeviceID, "3"); err != nil {
				fmt.Fprintf(progress, "warning: %s (%s) resumed, but its screen may lock: %v\n", g.lease.DeviceName, g.lease.serial(), firstLine(err))
			}
		}
		if g.boot == nil {
			return g.lease.DeviceID, nil
		}
		bootErr := boot(ctx, g.boot, g.wipe)
		// An interrupted boot is not the device's fault: the lease stays,
		// still booting, and the reap past bootTimeout decides whether the
		// device came up or died.
		if err := ctx.Err(); err != nil {
			return "", err
		}
		mine, err := settleBoot(session, platform, g.boot.Name, g.boot.PoweredOnAt, bootErr)
		if err != nil {
			return "", err
		}
		if !mine {
			return "", ErrLeaseLost
		}
		if bootErr == nil {
			return g.lease.DeviceID, nil
		}
		fmt.Fprintf(progress, "%s failed to boot (%v), recycling it; another device is tried as soon as the limit allows\n", g.boot.Name, bootErr)
	}
}

// settleBoot records a boot outcome only for the power-on it belongs to: in
// the unlocked boot window the lease can be released and the device leased
// again by another session, whose boot this outcome must not touch.
func settleBoot(session string, platform Platform, name string, poweredOnAt time.Time, bootErr error) (mine bool, err error) {
	err = WithLock(func(s *State) error {
		d := s.FindDevice(name)
		if d == nil || d.State != StateOn || !d.PoweredOnAt.Equal(poweredOnAt) {
			return errNoChange
		}
		l := s.FindLease(session, platform)
		mine = l != nil && l.DeviceName == name
		if bootErr == nil {
			d.Booting = false
			d.RecycleFailures = 0
			return nil
		}
		if mine {
			removeLease(s, l)
		}
		recordFailure(s, d, firstLine(bootErr), "boot-failed", clock())
		return nil
	})
	return mine, err
}

func leaveQueue(session string, platform Platform) {
	_ = WithLock(func(s *State) error {
		if !removeWaiters(s, session, platform) {
			return errNoChange
		}
		return nil
	})
}

func firstLine(err error) string {
	msg, _, _ := strings.Cut(err.Error(), "\n")
	return msg
}

func Pause(ctx context.Context, session string) error {
	var failures []error
	err := WithLock(func(s *State) error {
		now := clock()
		changed := reapLocked(ctx, s, now)
		changed = touchSession(s, session, now) > 0 || changed
		for _, l := range s.Leases {
			if l.SessionID != session || l.Suspended() {
				continue
			}
			if l.Physical {
				if err := pausePhysical(ctx, l); err != nil {
					failures = append(failures, err)
					continue
				}
				l.SuspendedAt = now
				changed = true
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

func RunRecycle(ctx context.Context, name string) error {
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
	return runRecycle(ctx, name, d.RecycleStartedAt)
}

// runRecycle acts only while the device is still in the recycle it was
// spawned for: a re-spawned worker restamps RecycleStartedAt, so a stale
// worker stops at its next step instead of touching a device leased since.
// The power-off runs under the lock so no lease can slip in between the
// check and the kill.
func runRecycle(ctx context.Context, name string, startedAt time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, RecycleBound)
	defer cancel()
	current := func(cur *Device) bool {
		return cur != nil && cur.State == StateRecycling && cur.RecycleStartedAt.Equal(startedAt)
	}
	var dev Device
	var stale bool
	var recycleErr error
	err := WithLock(func(s *State) error {
		cur := s.FindDevice(name)
		if !current(cur) {
			stale = true
			return errNoChange
		}
		dev = *cur
		if cur.PowerOffConfirmed {
			return errNoChange
		}
		if recycleErr = powerOff(ctx, cur); recycleErr != nil {
			return errNoChange
		}
		cur.PowerOffConfirmed = true
		s.journal(cur, StateRecycling, nil, "recycle-off")
		return nil
	})
	if err != nil {
		return err
	}
	if stale {
		return nil
	}
	if recycleErr == nil && dev.Platform == PlatformIOS {
		recycleErr = drv.eraseIOS(ctx, dev.UDID)
	}
	lockErr := WithLock(func(s *State) error {
		cur := s.FindDevice(name)
		if !current(cur) {
			return errNoChange
		}
		now := clock()
		if recycleErr != nil {
			recordFailure(s, cur, firstLine(recycleErr), "recycle-failed", now)
			return nil
		}
		markOff(cur)
		cur.RecycleStartedAt = time.Time{}
		cur.PowerOffConfirmed = false
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
	d.PowerOffConfirmed = false
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
		fmt.Fprintf(out, "warning: %s is in use (leased or recycling) — kept for now, will be removed on a later bank init\n", name)
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
			fmt.Fprintf(out, "warning: %s is in use (leased or recycling) — kept on its current profile, will be recreated on a later bank init\n", d.Name)
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
		fmt.Fprintf(out, "warning: %s is in use (leased or recycling) — kept for now, will be removed on a later bank init\n", name)
	}
	return nil
}

// StatusRow is one line of `atelierd device status`: a bank device or a
// physical device, with its lease or pending restore. Device is nil for a
// physical row; Absent marks a physical device the probe did not see.
type StatusRow struct {
	Name     string
	Platform Platform
	Physical bool
	State    DeviceState
	ID       string
	Serial   string
	Wired    bool
	Absent   bool
	Device   *Device
	Lease    *Lease
	Restore  *PendingRestore
}

func StatusRows(ctx context.Context) (rows []StatusRow, queue []*Waiter, err error) {
	s, err := Load()
	if err != nil {
		return nil, nil, err
	}
	for _, d := range s.Devices {
		rows = append(rows, StatusRow{
			Name: d.Name, Platform: d.Platform, State: d.State, ID: d.TargetID(), Device: d, Lease: s.leaseForDevice(d.Name),
		})
	}
	physical, _ := drv.physical(ctx, "")
	shownLeases := map[*Lease]bool{}
	shownRestores := map[*PendingRestore]bool{}
	for i := range physical {
		p := &physical[i]
		row := StatusRow{Name: p.Name, Platform: p.Platform, Physical: true, ID: p.ID, Serial: p.Serial, Wired: p.Wired}
		row.Lease, row.Restore = s.leaseForPhysical(p), s.restoreForPhysical(p)
		shownLeases[row.Lease], shownRestores[row.Restore] = true, true
		rows = append(rows, physicalRow(row))
	}
	for _, l := range s.Leases {
		if l.Physical && !shownLeases[l] {
			rows = append(rows, physicalRow(StatusRow{Name: l.DeviceName, Platform: l.Platform, Physical: true, ID: l.DeviceID, Serial: l.serial(), Absent: true, Lease: l}))
		}
	}
	for _, r := range s.Restores {
		if !shownRestores[r] {
			rows = append(rows, physicalRow(StatusRow{Name: r.Name, Platform: r.Platform, Physical: true, ID: r.DeviceID, Serial: r.Serial, Absent: true, Restore: r}))
		}
	}
	return rows, s.liveWaiters(clock(), ""), nil
}

func physicalRow(row StatusRow) StatusRow {
	row.State = StateOff
	if row.Lease != nil && !row.Lease.Suspended() {
		row.State = StateOn
	}
	return row
}
