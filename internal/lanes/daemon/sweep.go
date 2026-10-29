package daemon

import (
	"slices"
	"syscall"

	"github.com/valian-ca/homebrew-tools/internal/lanes/procinfo"
)

var (
	shells    = []string{"sh", "bash", "zsh", "fish", "dash", "nu"}
	editors   = []string{"vim", "nvim", "nano", "emacs", "code", "Code", "cursor", "Cursor", "Electron", "zed", "hx"}
	terminals = []string{"tmux", "screen", "zellij", "herdr", "login"}
	own       = []string{"lanes", "lanesd"}
)

// BuiltinSpare names what the sweep never stops: an agent's or developer's
// shells, editors and terminals, and lanes itself — a `lanes stop` run from
// the worktree must survive the teardown it asked for.
var BuiltinSpare = slices.Concat(shells, editors, terminals, own)

type Keep struct {
	Sessions []string
	Shared   []string
	Spare    []string
}

func SweepTargets(procs []procinfo.Proc, root string, keep Keep, self int) []procinfo.Proc {
	helpers := helpersOf(procs, slices.Concat(keep.Sessions, editors), true)
	for pid := range helpersOf(procs, own, false) {
		helpers[pid] = true
	}
	for pid := range launchersOf(procs, slices.Concat(keep.Sessions, editors, own)) {
		helpers[pid] = true
	}
	var out []procinfo.Proc
	for _, p := range procs {
		if p.PID == self || p.PID <= 1 || helpers[p.PID] || !p.Under(root) {
			continue
		}
		if p.NamedAs(BuiltinSpare) || p.NamedAs(keep.Sessions) || p.Runs(keep.Shared) || p.Runs(keep.Spare) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// helpersOf collects what the named processes launched, directly or through
// their own children. Under a session or an editor the descent stops at a
// shell: a dev server an agent or a developer starts from a shell is the very
// thing a teardown must stop. Under lanes it does not, since prep steps and
// adapter scripts run through /bin/sh.
func helpersOf(procs []procinfo.Proc, roots []string, stopAtShells bool) map[int]bool {
	children := map[int][]procinfo.Proc{}
	for _, p := range procs {
		children[p.PPID] = append(children[p.PPID], p)
	}
	out := map[int]bool{}
	var queue []int
	for _, p := range procs {
		if p.NamedAs(roots) {
			queue = append(queue, p.PID)
		}
	}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		for _, c := range children[pid] {
			if out[c.PID] || (stopAtShells && c.NamedAs(shells)) {
				continue
			}
			out[c.PID] = true
			queue = append(queue, c.PID)
		}
	}
	return out
}

// launchersOf collects the ancestors of the named processes: killing the
// launcher Claude Desktop starts a session under ends the session with it.
func launchersOf(procs []procinfo.Proc, roots []string) map[int]bool {
	byPID := procinfo.ByPID(procs)
	out := map[int]bool{}
	for _, p := range procs {
		if !p.NamedAs(roots) {
			continue
		}
		for pid := p.PPID; pid > 1 && !out[pid]; {
			out[pid] = true
			parent, ok := byPID[pid]
			if !ok {
				break
			}
			pid = parent.PPID
		}
	}
	return out
}

type signal struct {
	pid   int
	group bool
}

// Signals groups targets by process group: a group is signaled as a whole only
// when every member is a target, so a spared shell never shares the fate of a
// server it launched.
func Signals(procs, targets []procinfo.Proc, ownPGID int) []signal {
	isTarget := map[int]bool{}
	for _, t := range targets {
		isTarget[t.PID] = true
	}
	members := map[int][]int{}
	for _, p := range procs {
		members[p.PGID] = append(members[p.PGID], p.PID)
	}
	var out []signal
	done := map[int]bool{}
	for _, t := range targets {
		g := t.PGID
		whole := g > 1 && g != ownPGID && !slices.ContainsFunc(members[g], func(pid int) bool { return !isTarget[pid] })
		if whole {
			if !done[-g] {
				done[-g] = true
				out = append(out, signal{pid: g, group: true})
			}
			continue
		}
		out = append(out, signal{pid: t.PID})
	}
	return out
}

func send(kill func(int, syscall.Signal) error, sigs []signal, sig syscall.Signal) {
	for _, s := range sigs {
		pid := s.pid
		if s.group {
			pid = -pid
		}
		_ = kill(pid, sig)
	}
}

func procinfoByPID(procs []procinfo.Proc, pid int) (string, bool) {
	for _, p := range procs {
		if p.PID == pid {
			return p.Comm, true
		}
	}
	return "", false
}
