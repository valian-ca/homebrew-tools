package devicebank

import (
	"context"
	"time"
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
	physical(ctx context.Context, platform Platform) ([]PhysicalDevice, error)
	stayOn(ctx context.Context, id string) (string, error)
	setStayOn(ctx context.Context, id, value string) error
	removeReverses(ctx context.Context, id string) error
	addReverse(ctx context.Context, id, spec string) error
	clearApp(ctx context.Context, id, pkg string) error
	grantPermission(ctx context.Context, id, pkg, perm string) error
	apps(ctx context.Context, p PhysicalDevice) ([]string, error)
	appInfo(ctx context.Context, p PhysicalDevice, appID string) (AppInfo, error)
	androidLabel(ctx context.Context, id, pkg string) (string, error)
	spawnRecycle(name string) error
	spawnRestore(deviceID string) error
	notify(title, body string)
}

var (
	drv              driver = execDriver{}
	clock                   = time.Now
	queuePoll               = 2 * time.Second
	unpluggedRecheck        = 2 * time.Second
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

func (execDriver) physical(ctx context.Context, platform Platform) ([]PhysicalDevice, error) {
	return probePhysical(ctx, platform)
}

func (execDriver) stayOn(ctx context.Context, id string) (string, error) { return readStayOn(ctx, id) }

func (execDriver) setStayOn(ctx context.Context, id, value string) error {
	return writeStayOn(ctx, id, value)
}

func (execDriver) removeReverses(ctx context.Context, id string) error {
	return removeAdbReverses(ctx, id)
}

func (execDriver) addReverse(ctx context.Context, id, spec string) error {
	return addAdbReverse(ctx, id, spec)
}

func (execDriver) clearApp(ctx context.Context, id, pkg string) error { return pmClear(ctx, id, pkg) }

func (execDriver) grantPermission(ctx context.Context, id, pkg, perm string) error {
	return pmGrant(ctx, id, pkg, perm)
}

func (execDriver) apps(ctx context.Context, p PhysicalDevice) ([]string, error) {
	return installedApps(ctx, p)
}

func (execDriver) appInfo(ctx context.Context, p PhysicalDevice, appID string) (AppInfo, error) {
	return appInfo(ctx, p, appID)
}

func (execDriver) androidLabel(ctx context.Context, id, pkg string) (string, error) {
	return androidLabel(ctx, id, pkg)
}

func (execDriver) spawnRecycle(name string) error { return SpawnRecycle(name) }

func (execDriver) spawnRestore(deviceID string) error { return SpawnRestore(deviceID) }

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
