package devicebank

import "fmt"

// LeaseAnnotations maps each targetable device ID to the lease-state suffix
// frn's picker appends, so a human sees which devices a forge run holds
// before stepping on one. Lock-free snapshot read; no bank file means no
// annotations. frn never takes a lease — display only.
func LeaseAnnotations() map[string]string {
	if !Exists() {
		return nil
	}
	s, err := Load()
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, d := range s.Devices {
		out[d.TargetID()] = " — bank, " + string(d.State)
	}
	for _, l := range s.Leases {
		if l.Physical {
			out[l.DeviceID] = fmt.Sprintf(" — leased, session %s", l.SessionID)
		} else {
			out[l.DeviceID] = fmt.Sprintf(" — bank, leased, session %s", l.SessionID)
		}
	}
	return out
}

// AnnotateLabel appends the lease suffix for id, when one exists.
func AnnotateLabel(annotations map[string]string, label, id string) string {
	if suffix, ok := annotations[id]; ok {
		return label + suffix
	}
	return label
}
