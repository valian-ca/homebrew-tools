package devicebank

import (
	"time"
)

// PhysicalDevice is a live-probed physical device offered to the lease
// selector alongside the virtual bank.
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

// acquireExisting implements lease idempotence: a session that already holds
// a lease on the platform gets the same device back, renewed.
func acquireExisting(s *State, session string, platform Platform, now time.Time) *Lease {
	l := s.FindLease(session, platform)
	if l == nil {
		return nil
	}
	l.RenewedAt = now
	return l
}

// pickCandidate selects the next device to try, virtual bank first (booted
// free before cold), then unleased physical devices. Returns nil when the
// platform has nothing available.
func pickCandidate(s *State, platform Platform, physical []PhysicalDevice) *Candidate {
	for _, want := range []DeviceState{StateFree, StateOff} {
		for _, d := range s.Devices {
			if d.Platform == platform && d.State == want {
				return &Candidate{Device: d}
			}
		}
	}
	for i := range physical {
		p := &physical[i]
		if p.Platform == platform && s.LeaseForDeviceID(p.ID) == nil {
			return &Candidate{Physical: p}
		}
	}
	return nil
}

// commitLease records the lease for the candidate and flips a virtual
// device to leased.
func commitLease(s *State, c *Candidate, session, workdir string, platform Platform, now time.Time) *Lease {
	l := &Lease{
		SessionID:  session,
		Platform:   platform,
		Workdir:    workdir,
		AcquiredAt: now,
		RenewedAt:  now,
	}
	if c.Device != nil {
		c.Device.State = StateLeased
		c.Device.LastUsedAt = now
		l.DeviceName = c.Device.Name
		l.DeviceID = c.Device.TargetID()
	} else {
		l.DeviceID = c.Physical.ID
		l.DeviceName = c.Physical.Name
		l.Physical = true
	}
	s.Leases = append(s.Leases, l)
	return l
}

// touchSession renews every lease the session holds. Returns the number of
// leases touched.
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

// dropLease removes l from the lease list and returns the virtual device it
// held, flipped to recycling — or nil when the lease was physical (a
// physical device is leasable again the moment its lease is gone).
func dropLease(s *State, l *Lease, now time.Time) *Device {
	for i, cur := range s.Leases {
		if cur == l {
			s.Leases = append(s.Leases[:i], s.Leases[i+1:]...)
			break
		}
	}
	if l.Physical {
		return nil
	}
	d := s.FindDevice(l.DeviceName)
	if d == nil {
		return nil
	}
	d.State = StateRecycling
	d.RecycleStartedAt = now
	d.LastUsedAt = now
	return d
}

// releaseSession drops the session's leases (all platforms, or just one when
// platform is non-empty) and returns the virtual devices to recycle.
func releaseSession(s *State, session string, platform Platform, now time.Time) []*Device {
	var toRecycle []*Device
	for _, l := range append([]*Lease(nil), s.Leases...) {
		if l.SessionID != session {
			continue
		}
		if platform != "" && l.Platform != platform {
			continue
		}
		if d := dropLease(s, l, now); d != nil {
			toRecycle = append(toRecycle, d)
		}
	}
	return toRecycle
}

// reapExpired drops every lease whose last renewal is older than TTL and
// returns the virtual devices to recycle.
func reapExpired(s *State, now time.Time) []*Device {
	var toRecycle []*Device
	for _, l := range append([]*Lease(nil), s.Leases...) {
		if now.Sub(l.RenewedAt) <= TTL {
			continue
		}
		if d := dropLease(s, l, now); d != nil {
			toRecycle = append(toRecycle, d)
		}
	}
	return toRecycle
}

// idleDevices returns the free booted virtual devices idle past
// IdleShutdown — the lazy-shutdown set.
func idleDevices(s *State, now time.Time) []*Device {
	var idle []*Device
	for _, d := range s.Devices {
		if d.State == StateFree && now.Sub(d.LastUsedAt) > IdleShutdown {
			idle = append(idle, d)
		}
	}
	return idle
}

// stuckRecycles returns recycling devices whose recycler is presumed dead
// (no completion past StuckRecycle), to be re-spawned.
func stuckRecycles(s *State, now time.Time) []*Device {
	var stuck []*Device
	for _, d := range s.Devices {
		if d.State == StateRecycling && now.Sub(d.RecycleStartedAt) > StuckRecycle {
			stuck = append(stuck, d)
		}
	}
	return stuck
}
