package devicebank

import (
	"time"
)

type PhysicalDevice struct {
	ID       string
	Name     string
	Platform Platform
}

// Candidate is the lease selector's pick: exactly one of Device (virtual
// bank entry) or Physical is set.
type Candidate struct {
	Device   *Device
	Physical *PhysicalDevice
}

// recycleReapAfter gives a live worker, bounded by RecycleBound, the time
// to record its own outcome before reap presumes it dead.
const recycleReapAfter = RecycleBound + 30*time.Second

const maxRecycleFailures = 2

// commitLease records the lease for the candidate. A virtual device is on
// from this moment, so the ignition limit counts it before the unlocked
// boot that follows.
func commitLease(s *State, c *Candidate, session, workdir string, platform Platform, now time.Time) *Lease {
	l := &Lease{
		SessionID:  session,
		Platform:   platform,
		Workdir:    workdir,
		AcquiredAt: now,
		RenewedAt:  now,
	}
	if c.Device != nil {
		l.DeviceName = c.Device.Name
		l.DeviceID = c.Device.TargetID()
		powerUp(s, c.Device, l, now, "lease")
	} else {
		l.DeviceID = c.Physical.ID
		l.DeviceName = c.Physical.Name
		l.Physical = true
	}
	s.Leases = append(s.Leases, l)
	return l
}

func powerUp(s *State, d *Device, l *Lease, now time.Time, reason string) {
	from := d.State
	d.State = StateOn
	d.PoweredOnAt = now
	d.LastUsedAt = now
	d.Booting = true
	l.SuspendedAt = time.Time{}
	l.DeviceDied = false
	s.journal(d, from, l, reason)
}

func touchSession(s *State, session string, now time.Time) int {
	n := 0
	for _, l := range s.Leases {
		if l.SessionID == session {
			l.RenewedAt = now
			n++
		}
	}
	return n
}

func removeLease(s *State, l *Lease) {
	for i, cur := range s.Leases {
		if cur == l {
			s.Leases = append(s.Leases[:i], s.Leases[i+1:]...)
			return
		}
	}
}

// dropLease removes l and sends the virtual device it held to recycling,
// scheduling the worker. A physical device is leasable again the moment
// its lease is gone; a broken device stays broken.
func dropLease(s *State, l *Lease, now time.Time, reason string) {
	removeLease(s, l)
	if l.Physical {
		return
	}
	d := s.FindDevice(l.DeviceName)
	if d == nil || d.State == StateRecycling || d.State == StateBroken {
		return
	}
	from := d.State
	d.State = StateRecycling
	d.RecycleStartedAt = now
	d.RecycleOff = from == StateOff
	d.Booting = false
	d.LastUsedAt = now
	s.journal(d, from, nil, reason)
	s.spawnRecycle(d.Name)
}

// releaseSession drops the session's leases (all platforms, or just one when
// platform is non-empty) and its queue entries. Reports whether anything
// was dropped.
func releaseSession(s *State, session string, platform Platform, now time.Time, reason string) bool {
	dropped := false
	for _, l := range append([]*Lease(nil), s.Leases...) {
		if l.SessionID != session || (platform != "" && l.Platform != platform) {
			continue
		}
		dropLease(s, l, now, reason)
		dropped = true
	}
	return removeWaiters(s, session, platform) || dropped
}

func reapExpired(s *State, now time.Time) bool {
	reaped := false
	for _, l := range append([]*Lease(nil), s.Leases...) {
		if now.Sub(l.RenewedAt) > TTL {
			dropLease(s, l, now, "ttl")
			reaped = true
		}
	}
	return reaped
}

// recordFailure counts one recycle or boot failure against the device. The
// second failure since the last successful boot leaves it broken, out of
// the bank until bank init repairs it. Reports whether it broke.
func recordFailure(s *State, d *Device, cause, reason string, now time.Time) bool {
	from := d.State
	d.RecycleFailures++
	d.Booting = false
	if d.RecycleFailures >= maxRecycleFailures {
		d.State = StateBroken
		d.BrokenReason = cause
		d.RecycleStartedAt = time.Time{}
		s.journal(d, from, nil, "broken")
		return true
	}
	if from == StateOn {
		d.RecycleOff = false
	}
	d.State = StateRecycling
	d.RecycleStartedAt = now
	s.journal(d, from, nil, reason)
	s.spawnRecycle(d.Name)
	return false
}

func reapStuckRecycles(s *State, now time.Time) bool {
	stuck := false
	for _, d := range s.Devices {
		if d.State == StateRecycling && now.Sub(d.RecycleStartedAt) > recycleReapAfter {
			recordFailure(s, d, "recycle worker did not finish within 5m", "recycle-failed", now)
			stuck = true
		}
	}
	return stuck
}
