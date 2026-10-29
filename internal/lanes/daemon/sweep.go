package daemon

import (
	"context"
	"slices"
	"syscall"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/lanes/procinfo"
)

var (
	shells    = procinfo.Shells
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

// OrphanHolders returns what a crashed stack or a killed lanes test-env left
// holding one of the given listeners: a sweep target whose ancestry in the
// worktree, through sweep targets and shells — process-compose runs each
// piece under bash -c —, ends at a dead parent, with every sweep target that
// ancestor started. A holder whose chain reaches a live session, editor,
// terminal or lanes process is left alone: something alive still owns it.
func OrphanHolders(procs []procinfo.Proc, root string, keep Keep, self int, holders []int) []procinfo.Proc {
	allowed := procinfo.ByPID(SweepTargets(procs, root, keep, self))
	byPID := procinfo.ByPID(procs)
	orphanRoot := func(pid int) (procinfo.Proc, bool) {
		for {
			p, ok := byPID[pid]
			_, target := allowed[pid]
			if !ok || !p.Under(root) || (!target && !p.NamedAs(shells)) {
				return p, false
			}
			if p.PPID == 1 {
				return p, true
			}
			pid = p.PPID
		}
	}
	seen := map[int]bool{}
	var out []procinfo.Proc
	for _, pid := range holders {
		if _, target := allowed[pid]; !target {
			continue
		}
		p, ok := orphanRoot(pid)
		if !ok {
			continue
		}
		for _, d := range procinfo.Descendants(procs, p.PID) {
			if _, target := allowed[d.PID]; target && !seen[d.PID] {
				seen[d.PID] = true
				out = append(out, byPID[d.PID])
			}
		}
	}
	return out
}

// Orphans returns every sweep target a crashed stack left behind, listening
// or not: a watcher or a queue consumer would otherwise run beside its new
// copy until the next teardown.
func Orphans(procs []procinfo.Proc, root string, keep Keep, self int) []procinfo.Proc {
	var pids []int
	for _, t := range SweepTargets(procs, root, keep, self) {
		pids = append(pids, t.PID)
	}
	return OrphanHolders(procs, root, keep, self, pids)
}

// End stops targets with SIGTERM, by whole group where Signals allows it, and
// sends SIGKILL to those still alive after patience. A PID reused by another
// command in between is left alone.
func End(ctx context.Context, src procinfo.Source, kill func(int, syscall.Signal) error, procs, targets []procinfo.Proc, patience time.Duration) {
	send(kill, Signals(procs, targets, syscall.Getpgrp()), syscall.SIGTERM)
	deadline := time.Now().Add(patience)
	for {
		procs, err := src.Snapshot()
		if err != nil {
			return
		}
		alive := procinfo.ByPID(procs)
		var left []procinfo.Proc
		for _, t := range targets {
			if p, ok := alive[t.PID]; ok && p.Comm == t.Comm {
				left = append(left, p)
			}
		}
		if len(left) == 0 {
			return
		}
		if !time.Now().Before(deadline) {
			for _, p := range left {
				_ = kill(p.PID, syscall.SIGKILL)
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
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
