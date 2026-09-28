package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"syscall"
)

// ServiceLabels are the launchd labels `brew services` registers atelierd
// under, newest first. Homebrew 6 renamed service labels from
// homebrew.mxcl.<formula> to sh.brew.<formula>; the legacy label stays in the
// list so a machine on an older Homebrew keeps restarting correctly.
var ServiceLabels = []string{"sh.brew.atelierd", "homebrew.mxcl.atelierd"}

// launchctlPath is absolute because a launchd-spawned daemon has a minimal PATH.
const launchctlPath = "/bin/launchctl"

// pidLine matches the top-level `pid = N` line of `launchctl print` output.
var pidLine = regexp.MustCompile(`(?m)^\s*pid = (\d+)\s*$`)

// ErrNotLaunchdManaged means no known atelierd launchd job owns this process
// (a foreground `atelierd run`, a dev build, or a non-macOS host).
var ErrNotLaunchdManaged = errors.New("process is not the running instance of a known atelierd launchd job")

// Relauncher asks launchd to restart the daemon's own job onto the freshly
// installed binary. It exists because relying on KeepAlive after a clean exit
// leaves the respawn to launchd's scheduling: a job can sit loaded with
// `runs = 0` and a pending "speculative" spawn indefinitely, which is how a
// Homebrew upgrade left associates' daemons silently stopped. An explicit
// `launchctl kickstart -k` makes the restart deterministic.
type Relauncher struct {
	run    runner
	spawn  func(name string, args ...string) error
	uid    int
	pid    int
	labels []string
}

// NewRelauncher returns a Relauncher bound to the current process and user.
func NewRelauncher() *Relauncher {
	return &Relauncher{
		run:    execRun,
		spawn:  spawnDetached,
		uid:    os.Getuid(),
		pid:    os.Getpid(),
		labels: ServiceLabels,
	}
}

// Job returns the service target (gui/<uid>/<label>) of the launchd job whose
// running instance is this process. Matching on pid, rather than trusting
// XPC_SERVICE_NAME, guarantees a foreground `atelierd run` never kickstarts —
// and so never kills — some other job.
func (r *Relauncher) Job(ctx context.Context) (string, error) {
	for _, label := range r.labels {
		target := fmt.Sprintf("gui/%d/%s", r.uid, label)
		out, err := r.run(ctx, launchctlPath, "print", target)
		if err != nil {
			continue // job not loaded under this label
		}
		if pid, ok := parsePID(string(out)); ok && pid == r.pid {
			return target, nil
		}
	}
	return "", ErrNotLaunchdManaged
}

// Relaunch resolves this process's launchd job and starts a detached
// `launchctl kickstart -k` on it. launchd then delivers SIGTERM to this
// process — handled by the normal shutdown path — and spawns the new binary.
// It returns the target on success; on error the caller should fall back to
// exiting and relying on KeepAlive.
func (r *Relauncher) Relaunch(ctx context.Context) (string, error) {
	target, err := r.Job(ctx)
	if err != nil {
		return "", err
	}
	if err := r.spawn(launchctlPath, "kickstart", "-k", target); err != nil {
		return "", fmt.Errorf("spawn launchctl kickstart: %w", err)
	}
	return target, nil
}

// parsePID extracts the pid from `launchctl print` output. A loaded but
// stopped job has no pid line.
func parsePID(out string) (int, bool) {
	m := pidLine.FindStringSubmatch(out)
	if m == nil {
		return 0, false
	}
	pid, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return pid, true
}

// spawnDetached starts name in its own session so launchd tearing down this
// job's process group (on the SIGTERM the kickstart triggers) can't kill the
// launchctl call mid-flight. It is not bound to any context for the same
// reason: the daemon's root context is cancelled during that shutdown.
func spawnDetached(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }() // reap if we outlive it
	return nil
}
