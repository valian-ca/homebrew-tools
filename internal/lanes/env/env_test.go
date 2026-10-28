package env

import (
	"os/exec"
	"strings"
	"testing"
)

func lane() Lane {
	return Lane{ID: "feat_x_abc123", Worktree: "/w/it's here", Tmpdir: "/tmp/lanes-501-deadbeef", BlockStart: 20000, BlockSize: 10, PortNames: []string{"web", "admin"}}
}

func TestBuild(t *testing.T) {
	m := Build(lane()).Map()
	want := map[string]string{
		"LANE_ID":         "feat_x_abc123",
		"TMPDIR":          "/tmp/lanes-501-deadbeef",
		"PORT_WEB":        "20000",
		"PORT_ADMIN":      "20001",
		"LANES_PORT_BASE": "20000",
		"LANES_PORT_SIZE": "10",
		"LANES_VARS":      "LANE_ID LANES_WORKTREE TMPDIR PORT_WEB PORT_ADMIN LANES_PORT_BASE LANES_PORT_SIZE LANES_VARS",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %q, want %q", k, m[k], v)
		}
	}
}

func TestEnvironOverridesTMPDIR(t *testing.T) {
	out := Build(lane()).Environ([]string{"TMPDIR=/var/folders/claude", "HOME=/h"})
	var tmp []string
	for _, kv := range out {
		if strings.HasPrefix(kv, "TMPDIR=") {
			tmp = append(tmp, kv)
		}
	}
	if len(tmp) != 1 || tmp[0] != "TMPDIR=/tmp/lanes-501-deadbeef" {
		t.Fatalf("TMPDIR entries = %v", tmp)
	}
}

// The exports and the unset line round-trip through a real shell: nothing the
// core exported survives --unset, not even a port an earlier lane exported.
func TestExportsThenUnsetLeaveNothing(t *testing.T) {
	t.Setenv("LANES_VARS", "PORT_OLD")
	v := Build(lane())
	script := v.Exports() + `printf '%s|%s\n' "$LANES_WORKTREE" "$PORT_ADMIN"` + "\n" + Unsets(v) + "env\n"
	c := exec.Command("/bin/bash", "-c", script)
	c.Env = []string{"PATH=/usr/bin:/bin", "PORT_OLD=1"}
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	lines := strings.Split(string(out), "\n")
	if lines[0] != "/w/it's here|20001" {
		t.Fatalf("exports evaluated to %q", lines[0])
	}
	for _, l := range lines[1:] {
		name, _, _ := strings.Cut(l, "=")
		if strings.HasPrefix(name, "PORT_") || strings.HasPrefix(name, "LANE") || name == "TMPDIR" {
			t.Errorf("%s survived --unset", l)
		}
	}
}
