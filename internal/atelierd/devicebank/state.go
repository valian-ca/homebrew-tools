// Package devicebank owns the machine-local device bank: provisioned
// simulator/AVD clones, leases keyed by (session, platform), and the
// recycle lifecycle. See VAL-268.
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

// ParsePlatform validates a --platform flag value.
func ParsePlatform(s string) (Platform, error) {
	switch Platform(s) {
	case PlatformIOS, PlatformAndroid:
		return Platform(s), nil
	}
	return "", fmt.Errorf("invalid platform %q (want ios or android)", s)
}

type DeviceState string

const (
	StateFree      DeviceState = "free"
	StateLeased    DeviceState = "leased"
	StateRecycling DeviceState = "recycling"
	StateOff       DeviceState = "off"
)

// Lifecycle constants. TTL is the lease-renewal deadline; IdleShutdown is
// how long a free booted virtual device survives before lazy shutdown;
// StuckRecycle is when a recycling entry is presumed crashed and re-spawned.
const (
	TTL          = time.Hour
	IdleShutdown = 30 * time.Minute
	StuckRecycle = 15 * time.Minute
)

// Config is the bank sizing recorded by `bank init`.
type Config struct {
	IOS     int `json:"ios"`
	Android int `json:"android"`
}

// Device is one provisioned virtual bank device. Physical devices are never
// stored here — they are probed live and tracked only through leases.
type Device struct {
	Name             string      `json:"name"`
	Platform         Platform    `json:"platform"`
	UDID             string      `json:"udid,omitempty"`
	AVD              string      `json:"avd,omitempty"`
	Port             int         `json:"port,omitempty"`
	State            DeviceState `json:"state"`
	LastUsedAt       time.Time   `json:"lastUsedAt"`
	RecycleStartedAt time.Time   `json:"recycleStartedAt,omitempty"`
}

// TargetID returns the identifier a runner targets: simulator UDID for iOS,
// emulator serial for Android.
func (d *Device) TargetID() string {
	if d.Platform == PlatformIOS {
		return d.UDID
	}
	return fmt.Sprintf("emulator-%d", d.Port)
}

// Lease is one (session, platform) hold on a device.
type Lease struct {
	SessionID  string    `json:"sessionId"`
	Platform   Platform  `json:"platform"`
	DeviceName string    `json:"deviceName,omitempty"`
	DeviceID   string    `json:"deviceId"`
	Physical   bool      `json:"physical"`
	Workdir    string    `json:"workdir"`
	AcquiredAt time.Time `json:"acquiredAt"`
	RenewedAt  time.Time `json:"renewedAt"`
}

// State is the on-disk shape of ~/.atelier/devices.json.
type State struct {
	Config  Config    `json:"config"`
	Devices []*Device `json:"devices"`
	Leases  []*Lease  `json:"leases"`
}

// Initialized reports whether `bank init` has ever run.
func (s *State) Initialized() bool {
	return s.Config.IOS > 0 || s.Config.Android > 0 || len(s.Devices) > 0
}

// FindDevice returns the bank device with the given name, or nil.
func (s *State) FindDevice(name string) *Device {
	for _, d := range s.Devices {
		if d.Name == name {
			return d
		}
	}
	return nil
}

// FindLease returns the lease held by session on platform, or nil.
func (s *State) FindLease(session string, platform Platform) *Lease {
	for _, l := range s.Leases {
		if l.SessionID == session && l.Platform == platform {
			return l
		}
	}
	return nil
}

// LeaseForDeviceID returns the lease holding the given targetable ID, or nil.
func (s *State) LeaseForDeviceID(id string) *Lease {
	for _, l := range s.Leases {
		if l.DeviceID == id {
			return l
		}
	}
	return nil
}

// Exists reports whether the bank state file is present — the cheap guard
// emit uses to skip all lease work on machines without a bank.
func Exists() bool {
	_, err := os.Stat(paths.Devices())
	return err == nil
}

// Load reads the state snapshot without taking the lock. Display paths
// (status, frn) use this; mutations go through WithLock.
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
	return &s, nil
}

// save writes the state atomically (.tmp + rename), matching the outbox
// pattern. Callers must hold the flock.
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

// WithLock runs fn under the devices flock with a fresh load of the state,
// then persists the (possibly mutated) state if fn succeeds.
func WithLock(fn func(s *State) error) error {
	if err := paths.EnsureDir(paths.MustRoot()); err != nil {
		return err
	}
	lock := flock.New(paths.DevicesLock())
	if err := lock.Lock(); err != nil {
		return fmt.Errorf("acquire devices lock: %w", err)
	}
	defer func() { _ = lock.Unlock() }()

	s, err := Load()
	if err != nil {
		return err
	}
	if err := fn(s); err != nil {
		if errors.Is(err, errNoChange) {
			return nil
		}
		return err
	}
	return save(s)
}
