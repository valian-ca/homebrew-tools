//go:build darwin && cgo

package procinfo

/*
#include <errno.h>
#include <libproc.h>
#include <stdlib.h>
#include <string.h>
#include <sys/proc_info.h>
#include <sys/sysctl.h>
#include <netinet/in.h>

static int lanes_bsdinfo(int pid, int *ppid, int *pgid, char *name, int namelen) {
	struct proc_bsdinfo bi;
	if (proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &bi, sizeof(bi)) != sizeof(bi)) return -1;
	*ppid = (int)bi.pbi_ppid;
	*pgid = (int)bi.pbi_pgid;
	const char *n = bi.pbi_name[0] ? bi.pbi_name : bi.pbi_comm;
	strlcpy(name, n, namelen);
	return 0;
}

static int lanes_cwd(int pid, char *buf, int buflen) {
	struct proc_vnodepathinfo vi;
	if (proc_pidinfo(pid, PROC_PIDVNODEPATHINFO, 0, &vi, sizeof(vi)) != sizeof(vi)) return -1;
	strlcpy(buf, vi.pvi_cdir.vip_path, buflen);
	return 0;
}

static unsigned long long lanes_rss(int pid) {
	struct proc_taskinfo ti;
	if (proc_pidinfo(pid, PROC_PIDTASKINFO, 0, &ti, sizeof(ti)) != sizeof(ti)) return 0;
	return ti.pti_resident_size;
}

static int lanes_listens(int pid, int port) {
	int size = proc_pidinfo(pid, PROC_PIDLISTFDS, 0, NULL, 0);
	if (size <= 0) return 0;
	struct proc_fdinfo *fds = malloc(size);
	if (!fds) return 0;
	size = proc_pidinfo(pid, PROC_PIDLISTFDS, 0, fds, size);
	int n = size / (int)sizeof(struct proc_fdinfo);
	int found = 0;
	for (int i = 0; i < n && !found; i++) {
		if (fds[i].proc_fdtype != PROX_FDTYPE_SOCKET) continue;
		struct socket_fdinfo si;
		if (proc_pidfdinfo(pid, fds[i].proc_fd, PROC_PIDFDSOCKETINFO, &si, sizeof(si)) != sizeof(si)) continue;
		if (si.psi.soi_kind != SOCKINFO_TCP) continue;
		if (si.psi.soi_proto.pri_tcp.tcpsi_state != TSI_S_LISTEN) continue;
		if (ntohs((unsigned short)si.psi.soi_proto.pri_tcp.tcpsi_ini.insi_lport) == port) found = 1;
	}
	free(fds);
	return found;
}
*/
import "C"

import (
	"bytes"
	"encoding/binary"
	"unsafe"

	"golang.org/x/sys/unix"
)

type system struct{}

func System() Source { return system{} }

func listPIDs() []int {
	n := C.proc_listallpids(nil, 0)
	if n <= 0 {
		return nil
	}
	buf := make([]C.int, int(n)+64)
	n = C.proc_listallpids(unsafe.Pointer(&buf[0]), C.int(len(buf)*int(unsafe.Sizeof(buf[0]))))
	pids := make([]int, 0, int(n))
	for _, p := range buf[:max(0, int(n))] {
		if p > 0 {
			pids = append(pids, int(p))
		}
	}
	return pids
}

// Snapshot skips the processes whose cwd this user cannot read: they belong
// to other users, and lanes neither counts nor signals them.
func (system) Snapshot() ([]Proc, error) {
	var (
		name [64]C.char
		cwd  [C.MAXPATHLEN]C.char
	)
	var procs []Proc
	for _, pid := range listPIDs() {
		var ppid, pgid C.int
		if C.lanes_bsdinfo(C.int(pid), &ppid, &pgid, &name[0], C.int(len(name))) != 0 {
			continue
		}
		if C.lanes_cwd(C.int(pid), &cwd[0], C.int(len(cwd))) != 0 {
			continue
		}
		procs = append(procs, Proc{
			PID:  pid,
			PPID: int(ppid),
			PGID: int(pgid),
			Comm: C.GoString(&name[0]),
			Cwd:  C.GoString(&cwd[0]),
			RSS:  uint64(C.lanes_rss(C.int(pid))),
			Argv: argv(pid),
		})
	}
	return procs, nil
}

func (system) ListeningPID(port int) (int, bool) {
	for _, pid := range listPIDs() {
		if C.lanes_listens(C.int(pid), C.int(port)) != 0 {
			return pid, true
		}
	}
	return 0, false
}

// argv decodes KERN_PROCARGS2: a native-endian argc, the executable path,
// NUL padding, then argc NUL-terminated arguments.
func argv(pid int) []string {
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(raw) < 4 {
		return nil
	}
	argc := int(binary.LittleEndian.Uint32(raw[:4]))
	rest := raw[4:]
	i := bytes.IndexByte(rest, 0)
	if i < 0 {
		return nil
	}
	rest = rest[i:]
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}
	args := make([]string, 0, argc)
	for len(args) < argc && len(rest) > 0 {
		j := bytes.IndexByte(rest, 0)
		if j < 0 {
			args = append(args, string(rest))
			break
		}
		args = append(args, string(rest[:j]))
		rest = rest[j+1:]
	}
	return args
}
