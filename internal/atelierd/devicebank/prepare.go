package devicebank

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type Preparation struct {
	Reverses []string
	Clear    bool
	Grants   []string
}

var permissionName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z0-9_]+)+$`)

func checkReverse(spec string) error {
	port, ok := strings.CutPrefix(spec, "tcp:")
	if n, err := strconv.Atoi(port); !ok || err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
		return fmt.Errorf("invalid --reverse %q (want tcp:<port>)", spec)
	}
	return nil
}

// Prepare reaches the leased device and the lease's app id only: its
// arguments come from the skills, and a personal phone may be on the cable.
func Prepare(ctx context.Context, session string, platform Platform, prep Preparation) error {
	if platform != PlatformAndroid {
		return errors.New("prepare acts on a physical Android device only")
	}
	for _, spec := range prep.Reverses {
		if err := checkReverse(spec); err != nil {
			return err
		}
	}
	for _, perm := range prep.Grants {
		if !permissionName.MatchString(perm) {
			return fmt.Errorf("invalid --grant %q (want a permission name such as android.permission.CAMERA)", perm)
		}
	}
	var held Lease
	err := WithLock(func(s *State) error {
		touchSession(s, session, clock())
		l := s.FindLease(session, platform)
		switch {
		case l == nil:
			return fmt.Errorf("session %s holds no %s lease", session, platform)
		case !l.Physical:
			return fmt.Errorf("session %s leases %s, a virtual device: prepare acts on a physical device only", session, l.DeviceName)
		case l.Suspended():
			return fmt.Errorf("the lease on %s is suspended: lease again first", l.DeviceName)
		case l.AppID == "":
			return fmt.Errorf("the lease on %s has no --app-id", l.DeviceName)
		}
		held = *l
		return nil
	})
	if err != nil {
		return err
	}
	if prep.Clear {
		if err := drv.clearApp(ctx, held.DeviceID, held.AppID); err != nil {
			return fmt.Errorf("clear %s on %s: %w", held.AppID, held.DeviceName, err)
		}
	}
	for _, perm := range prep.Grants {
		if err := drv.grantPermission(ctx, held.DeviceID, held.AppID, perm); err != nil {
			return fmt.Errorf("grant %s to %s on %s: %w", perm, held.AppID, held.DeviceName, err)
		}
	}
	for _, spec := range prep.Reverses {
		if err := drv.addReverse(ctx, held.DeviceID, spec); err != nil {
			return fmt.Errorf("reverse %s on %s: %w", spec, held.DeviceName, err)
		}
	}
	return nil
}
