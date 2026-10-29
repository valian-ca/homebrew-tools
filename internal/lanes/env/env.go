package env

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
)

// Fixed lists every variable the core may export whatever the project
// declares, so --unset clears them even from a shell whose LANES_VARS is gone.
var Fixed = []string{"LANE_ID", "LANES_WORKTREE", "TMPDIR", "LANES_PORT_BASE", "LANES_PORT_SIZE", "LANES_VARS"}

type Var struct{ Name, Value string }

type Vars []Var

type Lane struct {
	ID         string
	Worktree   string
	Tmpdir     string
	BlockStart int
	BlockSize  int
	PortNames  []string
}

func PortVar(name string) string { return "PORT_" + strings.ToUpper(name) }

func Build(l Lane) Vars {
	v := Vars{
		{"LANE_ID", l.ID},
		{"LANES_WORKTREE", l.Worktree},
		{"TMPDIR", l.Tmpdir},
	}
	for i, n := range l.PortNames {
		v = append(v, Var{PortVar(n), strconv.Itoa(l.BlockStart + i)})
	}
	v = append(v,
		Var{"LANES_PORT_BASE", strconv.Itoa(l.BlockStart)},
		Var{"LANES_PORT_SIZE", strconv.Itoa(l.BlockSize)},
	)
	names := make([]string, 0, len(v)+1)
	for _, x := range v {
		names = append(names, x.Name)
	}
	names = append(names, "LANES_VARS")
	return append(v, Var{"LANES_VARS", strings.Join(names, " ")})
}

func (v Vars) Map() map[string]string {
	m := make(map[string]string, len(v))
	for _, x := range v {
		m[x.Name] = x.Value
	}
	return m
}

func (v Vars) Names() []string {
	names := make([]string, len(v))
	for i, x := range v {
		names[i] = x.Name
	}
	return names
}

// Environ overlays the lane's variables on base, replacing any inherited
// value, TMPDIR included.
func (v Vars) Environ(base []string) []string {
	m := v.Map()
	out := make([]string, 0, len(base)+len(v))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if _, ok := m[k]; !ok {
			out = append(out, kv)
		}
	}
	for _, x := range v {
		out = append(out, x.Name+"="+x.Value)
	}
	return out
}

func (v Vars) Exports() string {
	var b strings.Builder
	for _, x := range v {
		fmt.Fprintf(&b, "export %s=%s\n", x.Name, Quote(x.Value))
	}
	return b.String()
}

func Unsets(current Vars) string {
	names := slices.Clone(Fixed)
	names = append(names, current.Names()...)
	names = append(names, strings.Fields(os.Getenv("LANES_VARS"))...)
	slices.Sort(names)
	names = slices.Compact(names)
	return "unset " + strings.Join(names, " ") + "\n"
}

func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
