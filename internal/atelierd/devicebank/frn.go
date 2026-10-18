package devicebank

import (
	"fmt"
	"time"
)

// LeaseAnnotations maps each targetable device ID to the lease-state suffix
// frn's picker appends, so a human sees which devices a forge run holds —
// and who is queued behind it — before stepping on one. Lock-free snapshot
// read; no bank file means no annotations. frn never takes a lease.
func LeaseAnnotations() map[string]string {
	if !Exists() {
		return nil
	}
	s, err := Load()
	if err != nil {
		return nil
	}
	now := clock()
	out := map[string]string{}
	for _, d := range s.Devices {
		out[d.TargetID()] = " — bank, " + string(d.State)
	}
	for _, l := range s.Leases {
		switch {
		case l.Physical && l.Suspended():
			out[l.DeviceID] = fmt.Sprintf(" — leased, suspended, session %s", l.SessionID)
		case l.Physical:
			out[l.DeviceID] = fmt.Sprintf(" — leased, session %s", l.SessionID)
		case l.Suspended():
			out[l.DeviceID] = fmt.Sprintf(" — bank, suspended, session %s", l.SessionID)
		default:
			d := s.FindDevice(l.DeviceName)
			state := StateOn
			if d != nil {
				state = d.State
			}
			suffix := fmt.Sprintf(" — bank, %s, session %s", state, l.SessionID)
			if state == StateOn {
				if waiters := s.liveWaiters(now, l.SessionID); len(waiters) > 0 {
					head := waiters[0]
					suffix += fmt.Sprintf(" · %s waiting %d min", head.SessionID, int(now.Sub(head.EnqueuedAt)/time.Minute))
				}
			}
			out[l.DeviceID] = suffix
		}
	}
	return out
}

func AnnotateLabel(annotations map[string]string, label, id string) string {
	if suffix, ok := annotations[id]; ok {
		return label + suffix
	}
	return label
}
