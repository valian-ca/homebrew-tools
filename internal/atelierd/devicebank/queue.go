package devicebank

import (
	"fmt"
	"sort"
	"time"
)

func dropExpiredWaiters(s *State, now time.Time) bool {
	kept := s.Queue[:0]
	for _, w := range s.Queue {
		if !w.ExpiresAt.Before(now) {
			kept = append(kept, w)
		}
	}
	dropped := len(kept) != len(s.Queue)
	s.Queue = kept
	return dropped
}

func removeWaiters(s *State, session string, platform Platform) bool {
	kept := s.Queue[:0]
	for _, w := range s.Queue {
		if w.SessionID != session || (platform != "" && w.Platform != platform) {
			kept = append(kept, w)
		}
	}
	removed := len(kept) != len(s.Queue)
	s.Queue = kept
	return removed
}

func findWaiter(s *State, session string, platform Platform) *Waiter {
	for _, w := range s.Queue {
		if w.SessionID == session && w.Platform == platform {
			return w
		}
	}
	return nil
}

// upsertWaiter keeps the caller's place across calls: EnqueuedAt survives,
// the expiry only moves forward.
func upsertWaiter(s *State, session string, platform Platform, now, deadline time.Time) (*Waiter, bool) {
	expires := deadline.Add(QueueGrace)
	if w := findWaiter(s, session, platform); w != nil {
		if expires.After(w.ExpiresAt) {
			w.ExpiresAt = expires
			return w, true
		}
		return w, false
	}
	w := &Waiter{SessionID: session, Platform: platform, EnqueuedAt: now, ExpiresAt: expires}
	s.Queue = append(s.Queue, w)
	return w, true
}

func (s *State) orderedQueue() []*Waiter {
	q := append([]*Waiter(nil), s.Queue...)
	sort.SliceStable(q, func(i, j int) bool { return q[i].EnqueuedAt.Before(q[j].EnqueuedAt) })
	return q
}

// reservation is the virtual device a request would power on: the device
// its own suspended or dead lease already holds, else an unleased off
// device of the platform not yet promised to someone ahead.
func reservation(s *State, session string, platform Platform, taken map[string]bool) *Device {
	if l := s.FindLease(session, platform); l != nil {
		if l.Physical {
			return nil
		}
		if d := s.FindDevice(l.DeviceName); d != nil && d.State == StateOff {
			return d
		}
		return nil
	}
	for _, d := range s.Devices {
		if d.Platform == platform && d.State == StateOff && !taken[d.Name] && s.leaseForDevice(d.Name) == nil {
			return d
		}
	}
	return nil
}

// fairReservation applies the ignition limit and the queue order: walking
// the waiters ahead of the caller, each one the free slots and devices can
// serve takes its share first. A blocked waiter of another platform does
// not hold back a caller that can be served.
func fairReservation(s *State, session string, platform Platform) *Device {
	slots := s.maxOn() - s.poweredOnCount()
	taken := map[string]bool{}
	for _, w := range s.orderedQueue() {
		if w.SessionID == session && w.Platform == platform {
			break
		}
		if slots <= 0 {
			return nil
		}
		if d := reservation(s, w.SessionID, w.Platform, taken); d != nil {
			taken[d.Name] = true
			slots--
		}
	}
	if slots <= 0 {
		return nil
	}
	return reservation(s, session, platform, taken)
}

func hasVirtualCandidate(s *State, platform Platform) bool {
	return reservation(s, "", platform, nil) != nil
}

// waiterAhead reports whether a live waiter of the platform queued before
// the caller — it gets the physical fallback first.
func waiterAhead(s *State, session string, platform Platform) bool {
	for _, w := range s.orderedQueue() {
		if w.SessionID == session && w.Platform == platform {
			return false
		}
		if w.Platform == platform {
			return true
		}
	}
	return false
}

func pickPhysical(s *State, platform Platform, physical []PhysicalDevice) *PhysicalDevice {
	for i := range physical {
		p := &physical[i]
		if p.Platform == platform && s.LeaseForDeviceID(p.ID) == nil {
			return p
		}
	}
	return nil
}

func (s *State) heldOnDevice(session string) *Device {
	for _, l := range s.Leases {
		if l.SessionID != session || l.Physical || l.Suspended() || l.DeviceDied {
			continue
		}
		if d := s.FindDevice(l.DeviceName); d != nil && d.State == StateOn {
			return d
		}
	}
	return nil
}

func (s *State) liveWaiters(now time.Time, except string) []*Waiter {
	var out []*Waiter
	for _, w := range s.orderedQueue() {
		if w.SessionID != except && !w.ExpiresAt.Before(now) {
			out = append(out, w)
		}
	}
	return out
}

func HolderWarning(session string) string {
	if session == "" || !Exists() {
		return ""
	}
	s, err := Load()
	if err != nil {
		return ""
	}
	d := s.heldOnDevice(session)
	if d == nil {
		return ""
	}
	waiters := s.liveWaiters(clock(), session)
	if len(waiters) == 0 {
		return ""
	}
	head := waiters[0]
	return fmt.Sprintf("warning: session %s has been waiting for a device since %s (you hold %s, on since %s) — pause or release it when you can",
		head.SessionID, head.EnqueuedAt.Local().Format("15:04"), d.Name, d.PoweredOnAt.Local().Format("15:04"))
}
