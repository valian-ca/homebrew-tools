package ports

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/valian-ca/homebrew-tools/internal/lanes/fsx"
	"github.com/valian-ca/homebrew-tools/internal/lanes/paths"
)

// The range sits below macOS's ephemeral ports (49152+), so the kernel never
// hands one of a block's ports to an outgoing connection.
const (
	RangeLow  = 20000
	RangeHigh = 48999
)

type Entry struct {
	CommonDir string `json:"commonDir"`
	AdminDir  string `json:"adminDir"`
	Worktree  string `json:"worktree"`
	Project   string `json:"project"`
	Start     int    `json:"start"`
	Size      int    `json:"size"`
}

func (e Entry) End() int { return e.Start + e.Size - 1 }

func (e Entry) Contains(port int) bool { return port >= e.Start && port <= e.End() }

type Table struct {
	Entries []Entry `json:"entries"`
}

func Range() (int, int) {
	if v, ok := paths.TestOverride("LANES_PORT_RANGE"); ok {
		lo, hi, found := strings.Cut(v, "-")
		l, err1 := strconv.Atoi(lo)
		h, err2 := strconv.Atoi(hi)
		if found && err1 == nil && err2 == nil && l <= h {
			return l, h
		}
	}
	return RangeLow, RangeHigh
}

func Load() (Table, error) {
	var t Table
	_, err := fsx.ReadJSON(paths.Ports(), &t)
	return t, err
}

func Find(adminDir string) (Entry, bool, error) {
	t, err := Load()
	if err != nil {
		return Entry{}, false, err
	}
	for _, e := range t.Entries {
		if e.AdminDir == adminDir {
			return e, true, nil
		}
	}
	return Entry{}, false, nil
}

// Ensure returns the worktree's block, allocating one on its first pass. A
// block keeps its start for the worktree's whole life; it is reallocated only
// when the project asks for a larger block than it holds.
func Ensure(want Entry) (Entry, error) {
	var got Entry
	err := update(func(t *Table) error {
		for i, e := range t.Entries {
			if e.AdminDir != want.AdminDir {
				continue
			}
			if e.Size >= want.Size {
				got = e
				return nil
			}
			t.Entries = slices.Delete(t.Entries, i, i+1)
			break
		}
		start, err := allocate(t.Entries, want.Size)
		if err != nil {
			return err
		}
		want.Start = start
		t.Entries = append(t.Entries, want)
		got = want
		return nil
	})
	return got, err
}

func Remove(adminDir string) error {
	return update(func(t *Table) error {
		t.Entries = slices.DeleteFunc(t.Entries, func(e Entry) bool { return e.AdminDir == adminDir })
		return nil
	})
}

func update(fn func(*Table) error) error {
	if err := paths.EnsureDir(paths.MustStateDir()); err != nil {
		return err
	}
	return fsx.WithLock(paths.PortsLock(), func() error {
		t, err := Load()
		if err != nil {
			return err
		}
		if err := fn(&t); err != nil {
			return err
		}
		return fsx.WriteJSON(paths.Ports(), t)
	})
}

var ErrExhausted = errors.New("no free port block left in the range")

func allocate(entries []Entry, size int) (int, error) {
	lo, hi := Range()
	start := lo
	for start+size-1 <= hi {
		if e, ok := overlapping(entries, start, size); ok {
			start = e.End() + 1
			continue
		}
		if p, busy := firstBusy(start, size); busy {
			start = p + 1
			continue
		}
		return start, nil
	}
	return 0, fmt.Errorf("%w (%d-%d, block of %d)", ErrExhausted, lo, hi, size)
}

func overlapping(entries []Entry, start, size int) (Entry, bool) {
	end := start + size - 1
	for _, e := range entries {
		if start <= e.End() && e.Start <= end {
			return e, true
		}
	}
	return Entry{}, false
}

func firstBusy(start, size int) (int, bool) {
	for p := start; p < start+size; p++ {
		if Busy(p) {
			return p, true
		}
	}
	return 0, false
}

// Busy binds the wildcard address without SO_REUSEADDR, which BSD refuses
// while any socket holds the port on any address. Go's net.Listen sets
// SO_REUSEADDR and would let 127.0.0.1:p bind beside a 0.0.0.0:p listener.
func Busy(port int) bool {
	return bindFails(syscall.AF_INET, port) || bindFails(syscall.AF_INET6, port)
}

func bindFails(family, port int) bool {
	fd, err := syscall.Socket(family, syscall.SOCK_STREAM, 0)
	if err != nil {
		return false
	}
	defer syscall.Close(fd)
	var sa syscall.Sockaddr
	if family == syscall.AF_INET6 {
		_ = syscall.SetsockoptInt(fd, syscall.IPPROTO_IPV6, syscall.IPV6_V6ONLY, 1)
		sa = &syscall.SockaddrInet6{Port: port}
	} else {
		sa = &syscall.SockaddrInet4{Port: port}
	}
	return errors.Is(syscall.Bind(fd, sa), syscall.EADDRINUSE)
}
