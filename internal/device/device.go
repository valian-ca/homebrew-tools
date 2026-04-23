// Package device discovers connected Flutter-targetable devices.
// It shells out to adb, xcrun devicectl, and xcrun simctl in parallel.
package device

import (
	"context"
	"sync"
	"time"
)

// Nerd Font glyphs: android (U+F17B) and apple (U+F179). Stored as runes
// rather than literal UTF-8 so source normalisation by editors can't
// silently rewrite them.
const (
	glyphAndroid = ""
	glyphApple   = ""
)

// Kind distinguishes physical from virtual and by platform, driving the
// grouping in the picker.
type Kind int

const (
	KindAndroidUSB Kind = iota
	KindAndroidWiFi
	KindAndroidEmulator
	KindIOS
	KindSimulator
)

// Device is one row in the picker.
type Device struct {
	Label string // pretty, e.g. " Pixel 7 (usb)"
	ID    string // serial/udid passed to `flutter run -d`
	Kind  Kind
}

// IsPhysical groups usb/wifi android + iOS devicectl into the physical list.
func (d Device) IsPhysical() bool {
	switch d.Kind {
	case KindAndroidUSB, KindAndroidWiFi, KindIOS:
		return true
	}
	return false
}

// Result aggregates one probe cycle.
type Result struct {
	Physical   []Device
	Virtual    []Device
	AdbTimeout bool
}

// All flattens Physical then Virtual, preserving order.
func (r Result) All() []Device {
	out := make([]Device, 0, len(r.Physical)+len(r.Virtual))
	out = append(out, r.Physical...)
	out = append(out, r.Virtual...)
	return out
}

// FindByID returns the device with the given id, or false.
func (r Result) FindByID(id string) (Device, bool) {
	for _, d := range r.All() {
		if d.ID == id {
			return d, true
		}
	}
	return Device{}, false
}

// ProbeAll runs the three FAST probes in parallel under a 5s timeout.
// "Fast" means ~100ms wall-clock in the happy case: adb, devicectl (cached),
// simctl. xcdevice is intentionally excluded — it always blocks near the
// full --timeout. Use DeepProbeIOS separately for actively-scanned iOS.
//
// adb is the only probe that routinely hangs; when it does, AdbTimeout is
// set so the picker can offer a restart action.
func ProbeAll(ctx context.Context) Result {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var (
		wg         sync.WaitGroup
		mu         sync.Mutex
		res        Result
		adbHung    bool
	)

	run := func(fn func(context.Context) ([]Device, bool)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			devs, hung := fn(probeCtx)
			mu.Lock()
			defer mu.Unlock()
			if hung {
				adbHung = true
			}
			for _, d := range devs {
				if d.IsPhysical() {
					res.Physical = append(res.Physical, d)
				} else {
					res.Virtual = append(res.Virtual, d)
				}
			}
		}()
	}

	run(probeAndroid)
	run(probeIOS)
	run(probeSimulators)
	wg.Wait()

	res.AdbTimeout = adbHung
	return res
}
