package devicebank

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"
)

// Requirement is the device type a lease admits. The zero value is virtual:
// a caller that says nothing never gets a physical device.
type Requirement string

const (
	RequireVirtual  Requirement = "virtual"
	RequireAny      Requirement = "any"
	RequirePhysical Requirement = "physical"
)

func ParseRequirement(s string) (Requirement, error) {
	switch Requirement(s) {
	case RequireVirtual, RequireAny, RequirePhysical:
		return Requirement(s), nil
	}
	return "", fmt.Errorf("invalid type %q (want any, virtual or physical)", s)
}

func (r Requirement) admitsPhysical() bool { return r == RequireAny || r == RequirePhysical }

func (r Requirement) admitsVirtual() bool { return r != RequirePhysical }

type LeaseRequest struct {
	Session  string
	Workdir  string
	Platform Platform
	Type     Requirement
	AppID    string
	Wait     time.Duration
}

// physicalProbe is one pass of the bank's physical probe. A failed probe
// offers no candidate and never ends a lease.
type physicalProbe struct {
	ok      bool
	devices []PhysicalDevice
}

func (pp physicalProbe) wired(l *Lease) *PhysicalDevice {
	for i := range pp.devices {
		p := &pp.devices[i]
		if p.Wired && (p.ID == l.DeviceID || p.Serial == l.serial()) {
			return p
		}
	}
	return nil
}

const (
	restoreReapAfter  = 2 * time.Minute
	restoreRetryAfter = time.Minute
)

// holdPhysical decides an existing physical lease: kept while its device is
// still on the cable. A suspended Android lease resumes with its screen kept
// on again, outside the lock — only when the value to restore was recorded.
func holdPhysical(l *Lease, probe physicalProbe) (kept, resumeStayOn bool) {
	if !probe.ok {
		return true, false
	}
	if probe.wired(l) == nil {
		return false, false
	}
	if !l.Suspended() {
		return true, false
	}
	l.SuspendedAt = time.Time{}
	return true, l.Snapshot != nil && l.Snapshot.StayOnBefore != nil
}

func pickPhysical(s *State, platform Platform, devices []PhysicalDevice, excluded map[string]string, notes *[]string) *PhysicalDevice {
	var candidates []PhysicalDevice
	for _, p := range devices {
		if p.Platform == platform {
			candidates = append(candidates, p)
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	for i := range candidates {
		p := &candidates[i]
		if excluded[p.Serial] != "" {
			continue
		}
		var why string
		switch l, r := s.leaseForPhysical(p), s.restoreForPhysical(p); {
		case !p.Wired:
			why = "reachable without cable only"
		case l != nil:
			why = "leased by session " + l.SessionID
		case r != nil:
			why = "restore pending since " + r.EndedAt.Local().Format("15:04")
		default:
			return p
		}
		*notes = append(*notes, skipNote(p, why))
	}
	return nil
}

func skipNote(p *PhysicalDevice, why string) string {
	return fmt.Sprintf("note: %s (%s) skipped: %s", p.Name, p.Serial, why)
}

// takePhysical prepares a device just leased, outside the lock. The run's
// app already installed as a Store build makes the device unavailable before
// anything is touched: flutter run would uninstall it, data included. The
// snapshot is recorded before the screen setting changes, so a crash never
// leaves a change the restore does not know about. A non-empty skip note
// means the lease was given up and the caller chooses again.
func takePhysical(ctx context.Context, req LeaseRequest, taken *Lease, p PhysicalDevice) (skip string, err error) {
	mine := func(cur *Lease) bool {
		return cur != nil && cur.Physical && cur.DeviceID == p.ID && cur.AcquiredAt.Equal(taken.AcquiredAt)
	}
	giveUp := func(why string, touched bool) (string, error) {
		err := WithLock(func(s *State) error {
			cur := s.FindLease(req.Session, req.Platform)
			if !mine(cur) {
				return errNoChange
			}
			if touched {
				dropLease(s, cur, clock(), "take-failed")
			} else {
				removeLease(s, cur)
			}
			return nil
		})
		return skipNote(&p, why), err
	}

	app, err := drv.appInfo(ctx, p, req.AppID)
	if err == nil && app.Installed && !app.Developer && p.Platform == PlatformAndroid {
		app.Name, err = drv.androidLabel(ctx, p.ID, req.AppID)
	}
	if err != nil {
		return giveUp(fmt.Sprintf("could not read %s on the device (%s)", req.AppID, firstLine(err)), false)
	}
	if !app.overwritable() {
		return giveUp(req.AppID+" already installed and is neither a developer build nor named [D] …", false)
	}

	snap := &PhysicalSnapshot{TakenAt: clock()}
	if snap.AppsBefore, err = drv.apps(ctx, p); err != nil {
		return giveUp(fmt.Sprintf("could not list its apps (%s)", firstLine(err)), false)
	}
	if p.Platform == PlatformAndroid {
		before, err := drv.stayOn(ctx, p.ID)
		if err != nil {
			return giveUp(fmt.Sprintf("could not read %s (%s)", stayOnSetting, firstLine(err)), false)
		}
		snap.StayOnBefore = &before
	}

	lost := false
	if err := WithLock(func(s *State) error {
		cur := s.FindLease(req.Session, req.Platform)
		if !mine(cur) {
			lost = true
			return errNoChange
		}
		cur.Snapshot = snap
		return nil
	}); err != nil {
		return "", err
	}
	if lost {
		return "", ErrLeaseLost
	}

	if p.Platform == PlatformAndroid {
		if err := drv.setStayOn(ctx, p.ID, "3"); err != nil {
			return giveUp(fmt.Sprintf("could not keep its screen on (%s)", firstLine(err)), true)
		}
	}
	return "", nil
}

func pausePhysical(ctx context.Context, l *Lease) error {
	if l.Platform != PlatformAndroid || l.Snapshot == nil || l.Snapshot.StayOnBefore == nil {
		return nil
	}
	if err := drv.setStayOn(ctx, l.DeviceID, *l.Snapshot.StayOnBefore); err != nil {
		return fmt.Errorf("pause %s: %w", l.DeviceName, err)
	}
	return nil
}

// reapRestores re-spawns the worker of each due restore whose device is
// reachable again, by any transport: adb wifi is enough to restore. A device
// still out of reach is probed again at most once per retry period.
func reapRestores(ctx context.Context, s *State, now time.Time) bool {
	var due []*PendingRestore
	platforms := map[Platform]bool{}
	for _, r := range s.Restores {
		if now.Sub(r.StartedAt) > restoreReapAfter && now.Sub(r.LastAttemptAt) > restoreRetryAfter {
			due = append(due, r)
			platforms[r.Platform] = true
		}
	}
	if len(due) == 0 {
		return false
	}
	probed := map[Platform]physicalProbe{}
	for p := range platforms {
		devices, err := drv.physical(ctx, p)
		probed[p] = physicalProbe{ok: err == nil, devices: devices}
	}
	for _, r := range due {
		pp := probed[r.Platform]
		if !pp.ok || !slices.ContainsFunc(pp.devices, func(p PhysicalDevice) bool { return p.Serial == r.Serial || p.ID == r.DeviceID }) {
			r.LastAttemptAt = now
			r.LastError = r.Name + " is not connected"
			continue
		}
		r.StartedAt = now
		s.spawnRestore(r.DeviceID)
	}
	return true
}

func RunRestore(ctx context.Context, deviceID string) error {
	s, err := Load()
	if err != nil {
		return err
	}
	r := s.findRestore(deviceID)
	if r == nil {
		return nil
	}
	return runRestore(ctx, deviceID, r.StartedAt)
}

// runRestore acts only for the spawn it belongs to: a re-spawned worker
// restamps StartedAt, and a stale one records nothing.
func runRestore(ctx context.Context, deviceID string, startedAt time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, RecycleBound)
	defer cancel()
	s, err := Load()
	if err != nil {
		return err
	}
	r := s.findRestore(deviceID)
	if r == nil || !r.StartedAt.Equal(startedAt) {
		return nil
	}
	restoreErr := restorePhysical(ctx, *r)
	lockErr := WithLock(func(s *State) error {
		cur := s.findRestore(deviceID)
		if cur == nil || !cur.StartedAt.Equal(startedAt) {
			return errNoChange
		}
		if restoreErr == nil {
			s.removeRestore(cur)
			return nil
		}
		cur.LastAttemptAt = clock()
		cur.LastError = firstLine(restoreErr)
		return nil
	})
	if lockErr != nil {
		return lockErr
	}
	return restoreErr
}

// restorePhysical puts back the screen setting, removes the run's adb
// reverses, and uninstalls only developer builds that were not there before
// the lease: a Store app the user installed meanwhile stays. The screen
// setting is written back whenever it was recorded, even if the lease never
// changed it, so a crash between the change and its record cannot leak it.
func restorePhysical(ctx context.Context, r PendingRestore) error {
	devices, err := drv.physical(ctx, r.Platform)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(devices, func(p PhysicalDevice) bool { return p.Serial == r.Serial || p.ID == r.DeviceID })
	if i < 0 {
		return fmt.Errorf("%s is not connected", r.Name)
	}
	dev := devices[i]
	var errs []error
	if r.Platform == PlatformAndroid {
		if r.Snapshot != nil && r.Snapshot.StayOnBefore != nil {
			errs = append(errs, drv.setStayOn(ctx, dev.ID, *r.Snapshot.StayOnBefore))
		}
		errs = append(errs, drv.removeReverses(ctx, dev.ID))
	}
	if r.Snapshot == nil {
		return errors.Join(errs...)
	}
	now, err := drv.apps(ctx, dev)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, id := range now {
		if slices.Contains(r.Snapshot.AppsBefore, id) {
			continue
		}
		app, err := drv.appInfo(ctx, dev, id)
		switch {
		case err != nil:
			errs = append(errs, err)
		case app.Developer:
			errs = append(errs, drv.uninstall(ctx, dev, id))
		}
	}
	return errors.Join(errs...)
}
