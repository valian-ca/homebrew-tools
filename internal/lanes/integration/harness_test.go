//go:build integration && darwin

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/lanes/list"
	"github.com/valian-ca/homebrew-tools/internal/lanes/paths"
	"github.com/valian-ca/homebrew-tools/internal/lanes/pc"
	"github.com/valian-ca/homebrew-tools/internal/lanes/testutil"
	"github.com/valian-ca/homebrew-tools/internal/lanes/tmpdir"
)

var (
	binDir    string
	nextRange atomic.Int32
)

// The stand-ins for a session, an editor and the demo's shared process are a
// Go sleeper built under each name: macOS kills a renamed copy of /bin/sleep.
func TestMain(m *testing.M) {
	for _, kv := range testutil.GitEnv {
		k, v, _ := strings.Cut(kv, "=")
		os.Setenv(k, v)
	}
	dir, err := os.MkdirTemp("", "lanes-it-bin")
	if err != nil {
		panic(err)
	}
	binDir = dir
	root, _ := filepath.Abs("../../..")
	for _, tool := range []string{"lanes", "lanesd"} {
		c := exec.Command("go", "build", "-o", filepath.Join(dir, tool), "./cmd/"+tool)
		c.Dir = root
		if out, err := c.CombinedOutput(); err != nil {
			panic(fmt.Sprintf("build %s: %v\n%s", tool, err, out))
		}
	}
	for _, name := range []string{"claude", "nvim", "lanes-demo-shared"} {
		c := exec.Command("go", "build", "-o", filepath.Join(dir, name), "./testdata/sleeper")
		if out, err := c.CombinedOutput(); err != nil {
			panic(fmt.Sprintf("build %s: %v\n%s", name, err, out))
		}
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type harness struct {
	t        *testing.T
	state    string
	store    string
	env      []string
	main     string
	daemon   *exec.Cmd
	trees    []string
	children []*exec.Cmd
	lo       int
}

type option func(*harness)

func grace(d time.Duration) option {
	return func(h *harness) { h.env = append(h.env, "LANES_GRACE="+d.String()) }
}

func newHarness(t *testing.T, opts ...option) *harness {
	t.Helper()
	t.Parallel()
	state, err := os.MkdirTemp("/tmp", "lit")
	if err != nil {
		t.Fatal(err)
	}
	lo := 42000 + int(nextRange.Add(1)%69)*100
	h := &harness{t: t, state: state, store: testutil.RealTemp(t), lo: lo}
	h.env = append(os.Environ(), testutil.GitEnv...)
	h.env = append(h.env,
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"LANES_TEST=1",
		"LANES_STATE_DIR="+state,
		fmt.Sprintf("LANES_PORT_RANGE=%d-%d", lo, lo+99),
		"LANES_SCAN_INTERVAL=1s",
		"LANES_DEMO_STATE="+h.store,
	)
	for _, o := range opts {
		o(h)
	}
	if !strings.Contains(strings.Join(h.env, "\n"), "LANES_GRACE=") {
		h.env = append(h.env, "LANES_GRACE=3s")
	}

	h.main = h.repo("demo")
	t.Cleanup(h.cleanup)
	return h
}

// Every repo of a harness shares its demo store, as two clones of a project
// share a database server.
func (h *harness) repo(name string) string {
	h.t.Helper()
	dir := filepath.Join(testutil.RealTemp(h.t), name)
	h.run(filepath.Dir(dir), "../../../examples/lanes-demo/make-repo.sh", dir)
	return dir
}

func (h *harness) run(dir, name string, args ...string) string {
	h.t.Helper()
	if !filepath.IsAbs(name) && strings.Contains(name, "/") {
		abs, _ := filepath.Abs(name)
		name = abs
	}
	c := exec.Command(name, args...)
	c.Dir = dir
	c.Env = h.env
	out, err := c.CombinedOutput()
	if err != nil {
		h.t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

func (h *harness) git(dir string, args ...string) string {
	h.t.Helper()
	return strings.TrimSpace(h.run(dir, "git", args...))
}

type result struct {
	stdout, stderr string
	code           int
}

func (r result) String() string {
	return fmt.Sprintf("exit %d\nstdout:\n%s\nstderr:\n%s", r.code, r.stdout, r.stderr)
}

func (h *harness) lanesIn(dir string, stdin string, env []string, args ...string) result {
	h.t.Helper()
	c := exec.Command(filepath.Join(binDir, "lanes"), args...)
	c.Dir = dir
	c.Env = append(append([]string(nil), h.env...), env...)
	c.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	err := c.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		h.t.Fatal(err)
	}
	return result{out.String(), errb.String(), code}
}

func (h *harness) lanes(dir string, args ...string) result {
	h.t.Helper()
	return h.lanesIn(dir, "", nil, args...)
}

func (h *harness) mustLanes(dir string, args ...string) string {
	h.t.Helper()
	r := h.lanes(dir, args...)
	if r.code != 0 {
		h.t.Fatalf("lanes %s: %s", strings.Join(args, " "), r)
	}
	return r.stdout
}

func (h *harness) startDaemon() {
	h.t.Helper()
	c := exec.Command(filepath.Join(binDir, "lanesd"))
	c.Env = h.env
	logFile, err := os.OpenFile(filepath.Join(h.state, "lanesd.out"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		h.t.Fatal(err)
	}
	c.Stdout, c.Stderr = logFile, logFile
	if err := c.Start(); err != nil {
		h.t.Fatal(err)
	}
	h.daemon = c
	h.eventually("lanesd answers", 10*time.Second, func() bool {
		conn, err := net.Dial("unix", filepath.Join(h.state, "lanesd.sock"))
		if err == nil {
			conn.Close()
		}
		return err == nil
	})
}

func (h *harness) stopDaemon() {
	h.t.Helper()
	if h.daemon == nil {
		return
	}
	_ = h.daemon.Process.Signal(syscall.SIGTERM)
	_ = h.daemon.Wait()
	h.daemon = nil
}

func (h *harness) daemonLog() string {
	data, _ := os.ReadFile(filepath.Join(h.state, "lanesd.out"))
	return string(data)
}

// worktree adds a linked worktree of the demo on a new branch, laid out as
// claude --worktree lays it out.
func (h *harness) worktree(name, branch string) string {
	h.t.Helper()
	return h.worktreeIn(h.main, name, branch)
}

func (h *harness) worktreeIn(main, name, branch string) string {
	h.t.Helper()
	dir := filepath.Join(main, ".claude", "worktrees", name)
	h.git(main, "worktree", "add", "-q", "-b", branch, dir)
	h.trees = append(h.trees, dir)
	return dir
}

func (h *harness) adminDir(wt string) string {
	return h.git(wt, "rev-parse", "--path-format=absolute", "--git-dir")
}

func (h *harness) tmpdir(wt string) string {
	return tmpdir.Path(h.git(wt, "rev-parse", "--path-format=absolute", "--git-common-dir"), h.adminDir(wt))
}

func (h *harness) spawn(dir, name string, args ...string) *exec.Cmd {
	h.t.Helper()
	path := name
	if !filepath.IsAbs(name) {
		if _, err := os.Stat(filepath.Join(binDir, name)); err == nil {
			path = filepath.Join(binDir, name)
		}
	}
	c := exec.Command(path, args...)
	c.Dir = dir
	c.Env = h.env
	if err := c.Start(); err != nil {
		h.t.Fatal(err)
	}
	h.track(c)
	return c
}

var exits sync.Map

// track reaps a started child and records its exit, which alive reads without
// racing the reaper.
func (h *harness) track(c *exec.Cmd) {
	done := make(chan struct{})
	exits.Store(c, done)
	go func() {
		_ = c.Wait()
		close(done)
	}()
	h.children = append(h.children, c)
}

func (h *harness) session(dir string) *exec.Cmd { return h.spawn(dir, "claude", "600") }

func alive(c *exec.Cmd) bool {
	done, _ := exits.Load(c)
	select {
	case <-done.(chan struct{}):
		return false
	default:
		return syscall.Kill(c.Process.Pid, 0) == nil
	}
}

func (h *harness) cleanup() {
	for _, c := range h.children {
		_ = c.Process.Kill()
	}
	for _, wt := range h.trees {
		tmp := h.tmpdirSafe(wt)
		if tmp == "" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		c := pc.NewClient(paths.PCSocket(tmp))
		if c.Alive(ctx) {
			_ = c.Down(ctx)
		}
		cancel()
		os.RemoveAll(tmp)
	}
	h.stopDaemon()
	if h.t.Failed() {
		h.t.Logf("lanesd log:\n%s", h.daemonLog())
	}
	os.RemoveAll(h.state)
}

func (h *harness) tmpdirSafe(wt string) string {
	c := exec.Command("git", "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir")
	c.Dir = wt
	out, err := c.Output()
	if err != nil {
		return ""
	}
	l := strings.Fields(string(out))
	if len(l) != 2 {
		return ""
	}
	return tmpdir.Path(l[1], l[0])
}

func (h *harness) eventually(what string, timeout time.Duration, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out after %s waiting for: %s", timeout, what)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func listening(port int) bool {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 200*time.Millisecond)
	if err == nil {
		conn.Close()
	}
	return err == nil
}

func (h *harness) list() list.Report {
	h.t.Helper()
	var r list.Report
	if err := json.Unmarshal([]byte(h.mustLanes(h.main, "list", "--json")), &r); err != nil {
		h.t.Fatal(err)
	}
	return r
}

func (h *harness) laneOf(wt string) list.Lane {
	h.t.Helper()
	for _, l := range h.list().Lanes {
		if l.Worktree == wt {
			return l
		}
	}
	h.t.Fatalf("%s is not in lanes list", wt)
	return list.Lane{}
}

func (h *harness) port(wt, name string) int {
	h.t.Helper()
	return h.laneOf(wt).Ports["PORT_"+strings.ToUpper(name)]
}

func (h *harness) piecePID(wt, piece string) int {
	h.t.Helper()
	for _, p := range h.laneOf(wt).Pieces {
		if p.Name == piece {
			return p.PID
		}
	}
	return 0
}

// token reads the demo store's data for the worktree's current lane: the
// scripts adapter's out-of-worktree resource.
func (h *harness) token(wt string) string {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(h.store, h.laneOf(wt).Lane, "data.txt"))
	if err != nil {
		return ""
	}
	return string(data)
}

func (h *harness) storeHas(lane string) bool {
	_, err := os.Stat(filepath.Join(h.store, lane))
	return err == nil
}

func read(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}
