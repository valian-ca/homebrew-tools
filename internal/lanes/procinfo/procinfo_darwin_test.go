//go:build darwin && cgo

package procinfo

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSnapshotSeesAChildWithItsCwdAndArgv(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := exec.Command("/bin/sleep", "30")
	c.Dir = dir
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Process.Kill() }()

	procs, err := System().Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	p, ok := ByPID(procs)[c.Process.Pid]
	if !ok {
		t.Fatal("child not in the snapshot")
	}
	if p.Comm != "sleep" || p.Cwd != dir || p.PPID != os.Getpid() || p.RSS == 0 {
		t.Fatalf("child = %+v", p)
	}
	if len(p.Argv) != 2 || p.Argv[1] != "30" {
		t.Fatalf("argv = %q", p.Argv)
	}
	if got := Descendants(procs, os.Getpid()); len(got) < 2 {
		t.Fatalf("descendants of the test = %d", len(got))
	}
}

func TestListeningPIDFindsThisProcess(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	pid, ok := System().ListeningPID(ln.Addr().(*net.TCPAddr).Port)
	if !ok || pid != os.Getpid() {
		t.Fatalf("ListeningPID = %d, %v; want %d", pid, ok, os.Getpid())
	}
}
