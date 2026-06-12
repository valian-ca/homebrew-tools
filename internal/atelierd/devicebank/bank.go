package devicebank

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Bank device naming: atelier-<platform>-<index>, index 1-based.
const namePrefix = "atelier-"

// emulatorTopPort anchors the deterministic port scheme at the top of adb's
// auto-discovery range [5554, 5584] — ports outside it leave the emulator
// invisible to adb ("Requested adb port is outside the recommended range").
// Manually started emulators climb from 5554, so the bank descends from
// 5584: collisions would need ~15 simultaneous manual emulators.
const emulatorTopPort = 5584

func bankName(platform Platform, index int) string {
	return fmt.Sprintf("%s%s-%d", namePrefix, platform, index)
}

// EmulatorPort returns the deterministic console port for bank AVD index
// (1-based): 5584 - 2*(index-1), descending.
func EmulatorPort(index int) int {
	return emulatorTopPort - 2*(index-1)
}

// bankIndex extracts the 1-based index from a bank device name, or 0 when
// the name is not a bank name for the platform.
func bankIndex(platform Platform, name string) int {
	prefix := namePrefix + string(platform) + "-"
	if !strings.HasPrefix(name, prefix) {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimPrefix(name, prefix))
	if err != nil || n < 1 {
		return 0
	}
	return n
}

// ResizePlan is the outcome of comparing the existing bank against the
// requested size N for one platform.
type ResizePlan struct {
	Create []string // names to provision, ascending index
	Delete []string // free excess clones to remove, ascending index
	Keep   []string // leased excess clones that survive with a warning
}

// planResize computes the two-way sizing for one platform: indexes 1..n must
// exist; existing clones above n are deleted when free, kept (warned) when
// leased — a leased clone is never touched, per the ticket.
func planResize(s *State, platform Platform, n int) ResizePlan {
	var plan ResizePlan
	existing := map[int]*Device{}
	for _, d := range s.Devices {
		if d.Platform != platform {
			continue
		}
		if idx := bankIndex(platform, d.Name); idx > 0 {
			existing[idx] = d
		}
	}
	for i := 1; i <= n; i++ {
		if _, ok := existing[i]; !ok {
			plan.Create = append(plan.Create, bankName(platform, i))
		}
	}
	var excess []int
	for idx := range existing {
		if idx > n {
			excess = append(excess, idx)
		}
	}
	sort.Ints(excess)
	for _, idx := range excess {
		d := existing[idx]
		if d.State == StateLeased || d.State == StateRecycling {
			plan.Keep = append(plan.Keep, d.Name)
		} else {
			plan.Delete = append(plan.Delete, d.Name)
		}
	}
	return plan
}
