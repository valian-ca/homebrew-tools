package devicebank

import (
	"encoding/json"
	"os"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/paths"
)

// Transition is one line of ~/.atelier/device-transitions.jsonl. On and
// Leased describe the device after the transition, so the time a device
// spent powered on without a lease reads straight off consecutive lines.
type Transition struct {
	At       string      `json:"at"`
	Device   string      `json:"device"`
	Platform Platform    `json:"platform"`
	From     DeviceState `json:"from"`
	To       DeviceState `json:"to"`
	Session  string      `json:"session,omitempty"`
	Reason   string      `json:"reason"`
	On       bool        `json:"on"`
	Leased   bool        `json:"leased"`
}

func (s *State) journal(d *Device, from DeviceState, l *Lease, reason string) {
	t := Transition{
		At:       clock().UTC().Format("2006-01-02T15:04:05.000000000Z07:00"),
		Device:   d.Name,
		Platform: d.Platform,
		From:     from,
		To:       d.State,
		Reason:   reason,
		On:       d.mayBeOn(),
		Leased:   l != nil,
	}
	if l != nil {
		t.Session = l.SessionID
	}
	s.transitions = append(s.transitions, t)
}

// writeJournal is best-effort: a journal failure never fails a command.
func writeJournal(ts []Transition) {
	if len(ts) == 0 {
		return
	}
	f, err := os.OpenFile(paths.DeviceTransitions(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, paths.FileMode)
	if err != nil {
		return
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, t := range ts {
		_ = enc.Encode(t)
	}
}
