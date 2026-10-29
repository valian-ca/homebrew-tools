package firebase

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/lanes/config"
	"github.com/valian-ca/homebrew-tools/internal/lanes/procinfo"
)

const (
	metadataFile   = "firebase-export-metadata.json"
	readyBanner    = "All emulators ready"
	loadFailure    = "Failed to load function definition"
	workerMarker   = "functionsEmulatorRuntime"
	orphanPatience = 5 * time.Second
)

type RunSpec struct {
	Root      string
	Tmpdir    string
	Firebase  *config.Firebase
	PortNames []string
	Block     int
	Environ   []string
	Out       io.Writer
	Procs     procinfo.Source
}

type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }

func (e *exitError) ExitCode() int { return e.code }

func ReadyMarker(tmpdir string) string { return filepath.Join(tmpdir, "firebase", "ready") }

// ReadyProbe holds the marker to the process that wrote it: a piece killed
// with kill -9 leaves its marker behind, and the next one must not pass for
// ready on it.
const ReadyProbe = `kill -0 "$(cat "$TMPDIR/firebase/ready" 2>/dev/null)" 2>/dev/null`

// WorkDir is firebase-tools' cwd: its debug logs and the temporary
// directories of an export land there, inside the self-ignored .lanes.
func WorkDir(root string) string { return filepath.Join(root, ".lanes", "firebase") }

// Run is the firebase piece of the stack. It stays the parent of
// firebase-tools, so the stop is its own: one signal, the export into a
// staging directory, and the swap only once the export is complete.
func Run(ctx context.Context, s RunSpec) error {
	fb := s.Firebase
	ready := ReadyMarker(s.Tmpdir)
	if err := os.MkdirAll(filepath.Dir(ready), 0o700); err != nil {
		return err
	}
	_ = os.Remove(ready)
	cwd := WorkDir(s.Root)
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		return err
	}
	SweepOrphans(s.Procs, s.Root, s.Out)

	data := filepath.Join(s.Root, fb.Data)
	if err := settleData(data); err != nil {
		return err
	}
	if err := CheckSocket(s.Tmpdir); err != nil {
		return err
	}
	b, err := LoadBase(s.Root, fb)
	if err != nil {
		return err
	}
	plan := NewPlan(b, fb, Stack)
	ports := Ports(s.PortNames, s.Block)
	if missing := Missing(plan.RequiredPorts(), s.PortNames); len(missing) > 0 {
		return MissingError(missing)
	}
	bin, err := Binary(s.Root, fb)
	if err != nil {
		return err
	}
	caps, err := Caps(fb, s.Environ)
	if err != nil {
		return err
	}
	args := append(bin[1:], "emulators:start",
		"--config", ConfigPath(s.Root, fb, Stack),
		"--only", strings.Join(plan.Only(), ","),
		"--export-on-exit", data+".next")
	if from := importFrom(s.Root, fb, data); from != "" {
		args = append(args, "--import", from)
		fmt.Fprintf(s.Out, "firebase: importing %s\n", from)
	}

	cmd := exec.Command(bin[0], args...)
	cmd.Dir = cwd
	cmd.Env = append(append([]string(nil), s.Environ...), caps...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("firebase: %w", err)
	}
	exited := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		_ = pw.Close()
		close(exited)
	}()
	banner, failed := watch(pr, s.Out)

	var probe <-chan time.Time
	wasReady := false
	for {
		select {
		case <-banner:
			banner = nil
			probe = time.After(0)
		case <-probe:
			if allListening(plan, ports) {
				probe = nil
				failed = nil
				if err := os.WriteFile(ready, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
					return err
				}
				wasReady = true
				fmt.Fprintln(s.Out, "firebase: ready")
				continue
			}
			probe = time.After(250 * time.Millisecond)
		case <-failed:
			killTree(s.Procs, cmd.Process.Pid, s.Root)
			<-exited
			return &exitError{1, errors.New("firebase: a function failed to load — see lanes logs firebase")}
		case <-exited:
			killTree(s.Procs, cmd.Process.Pid, s.Root)
			return &exitError{exitCode(waitErr), fmt.Errorf("firebase-tools exited before the stop: %v", waitErr)}
		case <-ctx.Done():
			return stop(s, cmd, exited, data, wasReady)
		}
	}
}

func stop(s RunSpec, cmd *exec.Cmd, exited <-chan struct{}, data string, wasReady bool) error {
	_ = os.Remove(ReadyMarker(s.Tmpdir))
	timeout := s.Firebase.ExportTimeout.Std()
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-exited:
		switch {
		case wasReady && complete(data+".next"):
			if err := swap(data); err != nil {
				return err
			}
			fmt.Fprintf(s.Out, "firebase: exported to %s\n", data)
		case !wasReady:
			_ = os.RemoveAll(data + ".next")
			fmt.Fprintf(s.Out, "firebase: stopped before it was ready; %s keeps the last complete export\n", data)
		default:
			_ = os.RemoveAll(data + ".next")
			fmt.Fprintf(s.Out, "firebase: the export did not complete; %s keeps the last complete export\n", data)
		}
	case <-time.After(timeout):
		killTree(s.Procs, cmd.Process.Pid, s.Root)
		select {
		case <-exited:
		case <-time.After(orphanPatience):
		}
		if wasReady && complete(data+".next") {
			if err := swap(data); err != nil {
				return err
			}
			fmt.Fprintf(s.Out, "firebase: exported to %s (firebase-tools hung after the export)\n", data)
		} else {
			_ = os.RemoveAll(data + ".next")
			fmt.Fprintf(s.Out, "firebase: export abandoned after %s (export_timeout); %s keeps the last complete export\n", s.Firebase.ExportTimeout, data)
		}
	}
	killTree(s.Procs, cmd.Process.Pid, s.Root)
	return nil
}

func watch(r io.Reader, out io.Writer) (banner, failed <-chan struct{}) {
	b, f := make(chan struct{}), make(chan struct{})
	go func() {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		seenB, seenF := false, false
		for sc.Scan() {
			line := sc.Text()
			fmt.Fprintln(out, line)
			if !seenB && strings.Contains(line, readyBanner) {
				seenB = true
				close(b)
			}
			if !seenF && strings.Contains(line, loadFailure) {
				seenF = true
				close(f)
			}
		}
		_, _ = io.Copy(io.Discard, r)
	}()
	return b, f
}

func allListening(p Plan, ports map[string]int) bool {
	for _, n := range p.RequiredPorts() {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(SDKHost, strconv.Itoa(ports[n])), 200*time.Millisecond)
		if err != nil {
			return false
		}
		_ = conn.Close()
	}
	return true
}

func importFrom(root string, fb *config.Firebase, data string) string {
	if complete(data) {
		return data
	}
	if fb.Seed != "" {
		return filepath.Join(root, fb.Seed)
	}
	return ""
}

func complete(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, metadataFile))
	return err == nil
}

// settleData finishes a swap a crash interrupted. A staging directory that
// holds the metadata file is a complete export: firebase-tools writes it
// last, then moves the whole directory in place.
func settleData(data string) error {
	if complete(data + ".next") {
		if err := swap(data); err != nil {
			return err
		}
	}
	if _, err := os.Stat(data); errors.Is(err, os.ErrNotExist) && complete(data+".old") {
		if err := os.Rename(data+".old", data); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(data + ".next"); err != nil {
		return err
	}
	return os.RemoveAll(data + ".old")
}

func swap(data string) error {
	if _, err := os.Stat(data); err == nil {
		if err := os.RemoveAll(data + ".old"); err != nil {
			return err
		}
		if err := os.Rename(data, data+".old"); err != nil {
			return err
		}
	}
	if err := os.Rename(data+".next", data); err != nil {
		return err
	}
	return os.RemoveAll(data + ".old")
}

func Caps(fb *config.Firebase, environ []string) ([]string, error) {
	heap := fb.JVMHeap
	if v := lookup(environ, "LANES_FIREBASE_JVM_HEAP"); v != "" {
		h, err := config.ParseByteSize(v)
		if err != nil {
			return nil, fmt.Errorf("LANES_FIREBASE_JVM_HEAP: %w", err)
		}
		heap = h
	}
	timeout := fb.FunctionsTimeout.Std()
	if v := lookup(environ, "LANES_FIREBASE_FUNCTIONS_TIMEOUT"); v != "" {
		d, err := parseSeconds(v)
		if err != nil {
			return nil, fmt.Errorf("LANES_FIREBASE_FUNCTIONS_TIMEOUT: %w", err)
		}
		timeout = d
	}
	opts := "-Xmx" + xmx(heap)
	if cur := lookup(environ, "JAVA_TOOL_OPTIONS"); cur != "" {
		opts += " " + cur
	}
	seconds := int((timeout + time.Second - 1) / time.Second)
	return []string{"JAVA_TOOL_OPTIONS=" + opts, "FUNCTIONS_DISCOVERY_TIMEOUT=" + strconv.Itoa(seconds)}, nil
}

func xmx(b config.ByteSize) string {
	switch {
	case b%(1<<20) == 0:
		return strconv.FormatInt(int64(b)>>20, 10) + "m"
	case b%(1<<10) == 0:
		return strconv.FormatInt(int64(b)>>10, 10) + "k"
	}
	return strconv.FormatInt(int64(b), 10)
}

func parseSeconds(v string) (time.Duration, error) {
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
		return time.Duration(n) * time.Second, nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%q is not a duration (e.g. 90s, 3m, 180)", v)
	}
	return d, nil
}

func lookup(environ []string, name string) string {
	v := ""
	for _, kv := range environ {
		if k, val, ok := strings.Cut(kv, "="); ok && k == name {
			v = val
		}
	}
	return v
}

func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() > 0 {
		return ee.ExitCode()
	}
	return 1
}

// SweepOrphans ends what a killed firebase piece left behind: the processes
// that ran from WorkDir or are functions workers of the worktree, once their
// parent is gone, with everything they started. A live `lanes test-env` keeps
// its workers' parent.
func SweepOrphans(src procinfo.Source, root string, log io.Writer) {
	procs, err := src.Snapshot()
	if err != nil {
		return
	}
	set := map[int]bool{}
	for _, p := range procs {
		if p.PPID == 1 && (p.Under(WorkDir(root)) || isWorker(p, root)) {
			for _, d := range procinfo.Descendants(procs, p.PID) {
				set[d.PID] = true
			}
		}
	}
	if len(set) == 0 {
		return
	}
	targets := make([]int, 0, len(set))
	for pid := range set {
		targets = append(targets, pid)
	}
	fmt.Fprintf(log, "firebase: ending %d process(es) left by an earlier run\n", len(targets))
	terminate(targets)
}

func isWorker(p procinfo.Proc, root string) bool {
	return p.Under(root) && slices.ContainsFunc(p.Argv, func(a string) bool { return strings.Contains(a, workerMarker) })
}

// Descendants alone miss the emulators detached from firebase-tools and the workers already orphaned.
func killTree(src procinfo.Source, pid int, root string) {
	procs, err := src.Snapshot()
	if err != nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		return
	}
	set := map[int]bool{}
	for _, p := range procinfo.Descendants(procs, pid) {
		set[p.PID] = true
	}
	self := os.Getpid()
	for _, p := range procs {
		if p.PID != self && p.PPID == 1 && (p.Under(WorkDir(root)) || isWorker(p, root)) {
			set[p.PID] = true
		}
	}
	var targets []int
	for p := range set {
		targets = append(targets, p)
	}
	for _, p := range targets {
		_ = syscall.Kill(p, syscall.SIGKILL)
	}
	waitGone(targets, orphanPatience)
}

func terminate(targets []int) {
	for _, p := range targets {
		_ = syscall.Kill(p, syscall.SIGTERM)
	}
	if waitGone(targets, orphanPatience) {
		return
	}
	for _, p := range targets {
		_ = syscall.Kill(p, syscall.SIGKILL)
	}
	waitGone(targets, orphanPatience)
}

func waitGone(targets []int, patience time.Duration) bool {
	deadline := time.Now().Add(patience)
	for {
		alive := false
		for _, p := range targets {
			if syscall.Kill(p, 0) == nil {
				alive = true
				break
			}
		}
		if !alive {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}
