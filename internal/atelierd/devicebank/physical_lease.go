package devicebank

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"
)

// The zero Requirement is virtual: a caller that says nothing never takes a
// new physical device, so an old plugin can never touch a plugged phone.
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

// restoreReapAfter gives a live worker, bounded like a recycle, the time to
// record its own outcome before reap spawns another one.
const (
	restoreReapAfter  = RecycleBound + 30*time.Second
	restoreRetryAfter = time.Minute
)

type holdDecision int

const (
	holdKeep holdDecision = iota
	holdDrop
	holdRetake
	holdWait
)

// holdPhysical decides an existing physical lease. Only a probe that worked
// and no longer sees the device on the cable ends it. A lease whose
// preparation never finished (its preparer died, or a parallel call is still
// at it) is prepared again before it is handed back: the conflict check has
// not run on it.
func holdPhysical(l *Lease, probe physicalProbe) (decision holdDecision, dev *PhysicalDevice, resumeStayOn bool) {
	if probe.ok {
		if dev = probe.wired(l); dev == nil {
			return holdDrop, nil, false
		}
	}
	if l.AppID != "" && l.Snapshot == nil {
		if dev == nil {
			return holdWait, nil, false
		}
		l.SuspendedAt = time.Time{}
		return holdRetake, dev, false
	}
	if !l.Suspended() {
		return holdKeep, nil, false
	}
	l.SuspendedAt = time.Time{}
	// A lease recorded without its pre-lease value must never change the
	// setting: nothing would put it back.
	return holdKeep, nil, l.Snapshot != nil && l.Snapshot.StayOnBefore != nil
}

func pickPhysical(s *State, platform Platform, devices []PhysicalDevice, excluded map[string]bool, notes *[]string) *PhysicalDevice {
	var candidates []PhysicalDevice
	for _, p := range devices {
		if p.Platform == platform {
			candidates = append(candidates, p)
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	for i := range candidates {
		p := &candidates[i]
		if excluded[p.Serial] {
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

// takePhysical prepares a leased device outside the lock. A non-empty skip
// note means the lease was given up and the caller chooses again.
func takePhysical(ctx context.Context, taken *Lease, p PhysicalDevice) (skip string, err error) {
	mine := func(cur *Lease) bool {
		return cur != nil && cur.Physical && cur.DeviceID == p.ID && cur.AcquiredAt.Equal(taken.AcquiredAt)
	}
	giveUp := func(why string, touched bool) (string, error) {
		err := WithLock(func(s *State) error {
			cur := s.FindLease(taken.SessionID, taken.Platform)
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

	appID := taken.AppID
	app, err := drv.appInfo(ctx, p, appID)
	if err == nil && app.Installed && !app.Developer && p.Platform == PlatformAndroid {
		app.Name, err = drv.androidLabel(ctx, p.ID, appID)
	}
	if err != nil {
		return giveUp(fmt.Sprintf("could not read %s on the device (%s)", appID, firstLine(err)), false)
	}
	// flutter run would uninstall a Store build to install its own, data
	// included: the device is given up before anything on it is touched.
	if !app.overwritable() {
		return giveUp(appID+" already installed and is neither a developer build nor named [D] …", false)
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

	// Recorded before the setting changes, so a crash never leaves a change
	// the restore does not know about. A parallel preparer that recorded
	// first keeps its snapshot: this one would record its 3 as the value
	// from before the lease.
	lost, prepared := false, false
	if err := WithLock(func(s *State) error {
		cur := s.FindLease(taken.SessionID, taken.Platform)
		switch {
		case !mine(cur):
			lost = true
		case cur.Snapshot != nil:
			prepared = true
		default:
			cur.Snapshot = snap
			return nil
		}
		return errNoChange
	}); err != nil {
		return "", err
	}
	if lost {
		return "", ErrLeaseLost
	}
	if prepared {
		return "", nil
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

// reapRestores re-spawns the worker of each due restore. The worker probes
// for the device outside the lock: a probe here would hold the devices lock,
// which every emit takes, for up to its whole timeout.
func reapRestores(s *State, now time.Time) bool {
	due := false
	for _, r := range s.Restores {
		if now.Sub(r.StartedAt) > restoreReapAfter && now.Sub(r.LastAttemptAt) > restoreRetryAfter {
			r.StartedAt = now
			s.spawnRestore(r.DeviceID)
			due = true
		}
	}
	return due
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

// restorePhysical runs on any transport: adb wifi is enough to restore. It
// uninstalls the run's app alone: a restore can wait days for its device, and
// any other app installed meanwhile may be the user's own build.
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
			errs = append(errs, putStayOnBack(ctx, dev.ID, *r.Snapshot.StayOnBefore))
		}
		errs = append(errs, drv.removeReverses(ctx, dev.ID))
	}
	if r.Snapshot == nil || r.AppID == "" || slices.Contains(r.Snapshot.AppsBefore, r.AppID) {
		return errors.Join(errs...)
	}
	app, err := drv.appInfo(ctx, dev, r.AppID)
	switch {
	case err != nil:
		errs = append(errs, err)
	case app.Installed && app.Developer:
		errs = append(errs, drv.uninstall(ctx, dev, r.AppID))
	}
	return errors.Join(errs...)
}

// putStayOnBack writes the recorded value whenever the device holds another
// one — after a crash the bank cannot know whether its change happened — and
// never when it already holds it: a device that refuses the write would
// otherwise keep its restore pending forever.
func putStayOnBack(ctx context.Context, id, before string) error {
	if cur, err := drv.stayOn(ctx, id); err == nil && cur == before {
		return nil
	}
	return drv.setStayOn(ctx, id, before)
}

// ForgetRestore drops a pending restore without touching its device, which
// becomes leasable again: the way out for a device that refuses its restore.
func ForgetRestore(device string) error {
	found := false
	err := WithLock(func(s *State) error {
		for _, r := range append([]*PendingRestore(nil), s.Restores...) {
			if r.DeviceID == device || r.Serial == device {
				s.removeRestore(r)
				found = true
			}
		}
		if !found {
			return errNoChange
		}
		return nil
	})
	if err == nil && !found {
		return fmt.Errorf("no pending restore for %q", device)
	}
	return err
}
