package procinfo

import (
	"errors"
	"path/filepath"
	"strings"
)

type Proc struct {
	PID  int
	PPID int
	PGID int
	Comm string
	Argv []string
	Cwd  string
	RSS  uint64
}

// Names returns every name a process answers to: its kernel name and the
// basename of its argv[0], which differ for interpreters and for names longer
// than the kernel keeps.
func (p Proc) Names() []string {
	names := []string{p.Comm}
	if len(p.Argv) > 0 {
		if b := filepath.Base(p.Argv[0]); b != p.Comm {
			names = append(names, b)
		}
	}
	return names
}

// NamedAs ignores a login shell's leading dash.
func (p Proc) NamedAs(names []string) bool {
	for _, want := range names {
		for _, n := range p.Names() {
			if n == want || strings.TrimPrefix(n, "-") == want {
				return true
			}
		}
	}
	return false
}

// Runs reports whether the process is named as one of names, or one of its
// arguments ends with it as a path or a dotted class name — the shape a JVM
// daemon's main class takes on its command line.
func (p Proc) Runs(names []string) bool {
	if p.NamedAs(names) {
		return true
	}
	for _, want := range names {
		for _, a := range p.Argv[min(1, len(p.Argv)):] {
			if a == want || strings.HasSuffix(a, "/"+want) || strings.HasSuffix(a, "."+want) {
				return true
			}
		}
	}
	return false
}

func (p Proc) Under(dir string) bool {
	return p.Cwd != "" && (p.Cwd == dir || strings.HasPrefix(p.Cwd, dir+"/"))
}

type Source interface {
	Snapshot() ([]Proc, error)
	// ListeningPID finds the process listening on a TCP port, if this user
	// can see it.
	ListeningPID(port int) (int, bool)
}

var ErrUnsupported = errors.New("process inspection is only supported on macOS")

func ByPID(procs []Proc) map[int]Proc {
	m := make(map[int]Proc, len(procs))
	for _, p := range procs {
		m[p.PID] = p
	}
	return m
}

func Descendants(procs []Proc, root int) []Proc {
	children := map[int][]Proc{}
	var self *Proc
	for i, p := range procs {
		children[p.PPID] = append(children[p.PPID], p)
		if p.PID == root {
			self = &procs[i]
		}
	}
	if self == nil {
		return nil
	}
	out := []Proc{*self}
	for i := 0; i < len(out); i++ {
		if out[i].PID == 0 {
			continue
		}
		out = append(out, children[out[i].PID]...)
	}
	return out
}

type Fake struct {
	Procs     []Proc
	Listeners map[int]int
}

func (f *Fake) Snapshot() ([]Proc, error) { return append([]Proc(nil), f.Procs...), nil }

func (f *Fake) ListeningPID(port int) (int, bool) {
	pid, ok := f.Listeners[port]
	return pid, ok
}
