package devicebank

import (
	"context"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/device"
)

// driver is every toolchain call the lease lifecycle makes. Provisioning
// (bank init) stays on the exec functions directly.
type driver interface {
	bootIOS(ctx context.Context, udid string) error
	shutdownIOS(ctx context.Context, udid string) error
	eraseIOS(ctx context.Context, udid string) error
	avdBooted(ctx context.Context, serial string) bool
	startAVD(avd string, port int, wipe bool) error
	waitAVDReady(ctx context.Context, serial string) error
	killAVD(ctx context.Context, serial string) error
	poweredOn(ctx context.Context, ios, android bool) (map[string]bool, error)
	physical(ctx context.Context, platform Platform) []PhysicalDevice
	spawnRecycle(name string) error
	notify(title, body string)
}

var (
	drv       driver = execDriver{}
	clock            = time.Now
	queuePoll        = 2 * time.Second
)

type execDriver struct{}

func (execDriver) bootIOS(ctx context.Context, udid string) error { return BootSimulator(ctx, udid) }

func (execDriver) shutdownIOS(ctx context.Context, udid string) error {
	return ShutdownSimulator(ctx, udid)
}

func (execDriver) eraseIOS(ctx context.Context, udid string) error { return EraseSimulator(ctx, udid) }

func (execDriver) avdBooted(ctx context.Context, serial string) bool {
	return EmulatorBooted(ctx, serial)
}

func (execDriver) startAVD(avd string, port int, wipe bool) error {
	return StartEmulator(avd, port, wipe)
}

func (execDriver) waitAVDReady(ctx context.Context, serial string) error {
	return WaitEmulatorReady(ctx, serial)
}

func (execDriver) killAVD(ctx context.Context, serial string) error {
	return StopEmulator(ctx, serial)
}

func (execDriver) poweredOn(ctx context.Context, ios, android bool) (map[string]bool, error) {
	on := map[string]bool{}
	if ios {
		sims, err := ListSimulators(ctx)
		if err != nil {
			return nil, err
		}
		for _, sim := range sims {
			if sim.State != "Shutdown" {
				on[sim.UDID] = true
			}
		}
	}
	if android {
		serials, err := AdbSerials(ctx)
		if err != nil {
			return nil, err
		}
		for _, serial := range serials {
			on[serial] = true
		}
	}
	return on, nil
}

// physical converts frn's physical-device probe into lease candidates for
// one platform. Bank AVDs show up in adb as emulators, not physical, so the
// probe never aliases a bank device.
func (execDriver) physical(ctx context.Context, platform Platform) []PhysicalDevice {
	res := device.ProbeAll(ctx)
	var out []PhysicalDevice
	for _, d := range res.Physical {
		p := PlatformAndroid
		if d.Kind == device.KindIOS {
			p = PlatformIOS
		}
		if p == platform {
			out = append(out, PhysicalDevice{ID: d.ID, Name: d.Label, Platform: p})
		}
	}
	return out
}

func (execDriver) spawnRecycle(name string) error { return SpawnRecycle(name) }

func (execDriver) notify(title, body string) { Notify(title, body) }

func powerOff(ctx context.Context, d *Device) error {
	if d.Platform == PlatformIOS {
		return drv.shutdownIOS(ctx, d.UDID)
	}
	return drv.killAVD(ctx, d.TargetID())
}

// boot starts a device the state already records as on. A recycled AVD is
// wiped on this boot: recycling never boots, so the emulator's own
// -wipe-data is the erase.
func boot(ctx context.Context, d *Device, wipe bool) error {
	if d.Platform == PlatformIOS {
		return drv.bootIOS(ctx, d.UDID)
	}
	serial := d.TargetID()
	if !wipe && drv.avdBooted(ctx, serial) {
		return nil
	}
	_ = drv.killAVD(ctx, serial)
	if err := drv.startAVD(d.AVD, d.Port, wipe); err != nil {
		return err
	}
	return drv.waitAVDReady(ctx, serial)
}
