package devicebank

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/gofrs/flock"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/paths"
)

type Platform string

const (
	PlatformIOS     Platform = "ios"
	PlatformAndroid Platform = "android"
)

func ParsePlatform(s string) (Platform, error) {
	switch Platform(s) {
	case PlatformIOS, PlatformAndroid:
		return Platform(s), nil
	}
	return "", fmt.Errorf("invalid platform %q (want ios or android)", s)
}

type DeviceState string

const (
	StateOff       DeviceState = "off"
	StateOn        DeviceState = "on"
	StateRecycling DeviceState = "recycling"
	StateBroken    DeviceState = "broken"
)

// States written by atelierd 0.20 and earlier, rewritten on load.
const (
	legacyStateFree   DeviceState = "free"
	legacyStateLeased DeviceState = "leased"
)

const (
	TTL          = time.Hour
	RecycleBound = 5 * time.Minute
	QueueGrace   = 2 * time.Minute
	NotifyAfter  = 10 * time.Minute
	DefaultMaxOn = 1
)

type Config struct {
	IOS     int `json:"ios"`
	Android int `json:"android"`
	MaxOn   int `json:"maxOn,omitempty"`
}

type Device struct {
	Name              string      `json:"name"`
	Platform          Platform    `json:"platform"`
	UDID              string      `json:"udid,omitempty"`
	AVD               string      `json:"avd,omitempty"`
	Port              int         `json:"port,omitempty"`
	State             DeviceState `json:"state"`
	LastUsedAt        time.Time   `json:"lastUsedAt"`
	PoweredOnAt       time.Time   `json:"poweredOnAt,omitzero"`
	Booting           bool        `json:"booting,omitempty"`
	RecycleStartedAt  time.Time   `json:"recycleStartedAt,omitzero"`
	PowerOffConfirmed bool        `json:"powerOffConfirmed,omitempty"`
	PowerOffFailedAt  time.Time   `json:"powerOffFailedAt,omitzero"`
	RecycleFailures   int         `json:"recycleFailures,omitempty"`
	BrokenReason      string      `json:"brokenReason,omitempty"`
	WipePending       bool        `json:"wipePending,omitempty"`
}

func (d *Device) TargetID() string {
	if d.Platform == PlatformIOS {
		return d.UDID
	}
	return fmt.Sprintf("emulator-%d", d.Port)
}

// mayBeOn is what the ignition limit counts. A recycling or broken device
// counts until its power-off is confirmed: a release racing a lease, or a
// device that broke while running, can never let a second device on.
func (d *Device) mayBeOn() bool {
	switch d.State {
	case StateOn:
		return true
	case StateRecycling, StateBroken:
		return !d.PowerOffConfirmed
	}
	return false
}

type Lease struct {
	SessionID   string            `json:"sessionId"`
	Platform    Platform          `json:"platform"`
	DeviceName  string            `json:"deviceName,omitempty"`
	DeviceID    string            `json:"deviceId"`
	Physical    bool              `json:"physical"`
	Serial      string            `json:"serial,omitempty"`
	AppID       string            `json:"appId,omitempty"`
	Snapshot    *PhysicalSnapshot `json:"snapshot,omitempty"`
	Workdir     string            `json:"workdir"`
	AcquiredAt  time.Time         `json:"acquiredAt"`
	RenewedAt   time.Time         `json:"renewedAt"`
	SuspendedAt time.Time         `json:"suspendedAt,omitzero"`
	DeviceDied  bool              `json:"deviceDied,omitempty"`
}

// serial falls back to the target id for a lease written before the bank
// recorded serials.
func (l *Lease) serial() string {
	if l.Serial != "" {
		return l.Serial
	}
	return l.DeviceID
}

type PhysicalSnapshot struct {
	TakenAt      time.Time `json:"takenAt"`
	StayOnBefore *string   `json:"stayOnBefore,omitempty"`
	AppsBefore   []string  `json:"appsBefore"`
}

// PendingRestore keeps its device out of every lease until it succeeds.
type PendingRestore struct {
	DeviceID      string            `json:"deviceId"`
	Serial        string            `json:"serial"`
	Name          string            `json:"name"`
	Platform      Platform          `json:"platform"`
	AppID         string            `json:"appId,omitempty"`
	Snapshot      *PhysicalSnapshot `json:"snapshot,omitempty"`
	EndedAt       time.Time         `json:"endedAt"`
	Reason        string            `json:"reason"`
	StartedAt     time.Time         `json:"startedAt,omitzero"`
	LastAttemptAt time.Time         `json:"lastAttemptAt,omitzero"`
	LastError     string            `json:"lastError,omitempty"`
}

func (l *Lease) Suspended() bool { return !l.SuspendedAt.IsZero() }

type Waiter struct {
	SessionID  string    `json:"sessionId"`
	Platform   Platform  `json:"platform"`
	EnqueuedAt time.Time `json:"enqueuedAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
	NotifiedAt time.Time `json:"notifiedAt,omitzero"`
}

// State is the on-disk shape of ~/.atelier/devices.json.
type State struct {
	Config   Config            `json:"config"`
	Devices  []*Device         `json:"devices"`
	Leases   []*Lease          `json:"leases"`
	Queue    []*Waiter         `json:"queue,omitempty"`
	Restores []*PendingRestore `json:"restores,omitempty"`

	transitions []Transition
	// Spawned only once the state is saved and the lock released: a worker's
	// first act is to take the same lock.
	spawns []spawnJob
}

type spawnJob struct {
	restore bool
	target  string
}

func (s *State) Initialized() bool {
	return s.Config.IOS > 0 || s.Config.Android > 0 || len(s.Devices) > 0
}

func (s *State) FindDevice(name string) *Device {
	for _, d := range s.Devices {
		if d.Name == name {
			return d
		}
	}
	return nil
}

func (s *State) FindLease(session string, platform Platform) *Lease {
	for _, l := range s.Leases {
		if l.SessionID == session && l.Platform == platform {
			return l
		}
	}
	return nil
}

func (s *State) LeaseForDeviceID(id string) *Lease {
	for _, l := range s.Leases {
		if l.DeviceID == id {
			return l
		}
	}
	return nil
}

func (s *State) leaseForPhysical(p *PhysicalDevice) *Lease {
	for _, l := range s.Leases {
		if l.Physical && (l.DeviceID == p.ID || l.serial() == p.Serial) {
			return l
		}
	}
	return nil
}

func (s *State) findRestore(deviceID string) *PendingRestore {
	for _, r := range s.Restores {
		if r.DeviceID == deviceID {
			return r
		}
	}
	return nil
}

func (s *State) restoreForPhysical(p *PhysicalDevice) *PendingRestore {
	for _, r := range s.Restores {
		if r.DeviceID == p.ID || r.Serial == p.Serial {
			return r
		}
	}
	return nil
}

func (s *State) removeRestore(r *PendingRestore) {
	for i, cur := range s.Restores {
		if cur == r {
			s.Restores = append(s.Restores[:i], s.Restores[i+1:]...)
			return
		}
	}
}

func (s *State) leaseForDevice(name string) *Lease {
	for _, l := range s.Leases {
		if !l.Physical && l.DeviceName == name {
			return l
		}
	}
	return nil
}

func (s *State) maxOn() int {
	if s.Config.MaxOn > 0 {
		return s.Config.MaxOn
	}
	return DefaultMaxOn
}

func (s *State) poweredOnCount() int {
	n := 0
	for _, d := range s.Devices {
		if d.mayBeOn() {
			n++
		}
	}
	return n
}

func (s *State) spawnRecycle(name string) {
	s.spawns = append(s.spawns, spawnJob{target: name})
}

func (s *State) spawnRestore(deviceID string) {
	s.spawns = append(s.spawns, spawnJob{restore: true, target: deviceID})
}

// migrate rewrites a 0.20 state in place. A legacy free device was booted
// without a lease: it becomes on, and the next reap powers it off.
func (s *State) migrate() {
	for _, d := range s.Devices {
		switch d.State {
		case legacyStateLeased:
			d.State = StateOn
			if l := s.leaseForDevice(d.Name); l != nil {
				d.PoweredOnAt = l.AcquiredAt
			}
		case legacyStateFree:
			d.State = StateOn
			d.PoweredOnAt = d.LastUsedAt
			s.journal(d, legacyStateFree, nil, "migrate")
		}
	}
}

// Exists reports whether the bank state file is present — the cheap guard
// emit uses to skip all lease work on machines without a bank.
func Exists() bool {
	_, err := os.Stat(paths.Devices())
	return err == nil
}

// Load takes no lock: never write back what it returns, go through WithLock.
func Load() (*State, error) {
	data, err := os.ReadFile(paths.Devices())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &State{}, nil
		}
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", paths.Devices(), err)
	}
	s.migrate()
	return &s, nil
}

func save(s *State) error {
	if err := paths.EnsureDir(paths.MustRoot()); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	target := paths.Devices()
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, paths.FileMode); err != nil {
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// errNoChange is returned by a WithLock fn that left the state untouched —
// the write is skipped but the call succeeds. Keeps hot paths (every emit
// renews leases) from rewriting the file when there is nothing to renew.
var errNoChange = errors.New("devicebank: no change")

func WithLock(fn func(s *State) error) error {
	if err := paths.EnsureDir(paths.MustRoot()); err != nil {
		return err
	}
	lock := flock.New(paths.DevicesLock())
	if err := lock.Lock(); err != nil {
		return fmt.Errorf("acquire devices lock: %w", err)
	}
	s, err := Load()
	if err != nil {
		_ = lock.Unlock()
		return err
	}
	if err := fn(s); err != nil {
		_ = lock.Unlock()
		if errors.Is(err, errNoChange) {
			return nil
		}
		return err
	}
	if err := save(s); err != nil {
		_ = lock.Unlock()
		return err
	}
	writeJournal(s.transitions)
	_ = lock.Unlock()
	for _, job := range s.spawns {
		if job.restore {
			_ = drv.spawnRestore(job.target)
		} else {
			_ = drv.spawnRecycle(job.target)
		}
	}
	return nil
}
