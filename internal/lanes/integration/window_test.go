//go:build integration && darwin

package integration

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Each harness gives lanes a window of 100 ports out of 42000-48899. Two
// go test runs on one Mac draw from the same windows, so a test claims its
// window with a flock on a file every run sees. The kernel drops the lock
// when the run's process exits, killed or not, and Go opens the file
// close-on-exec, so a lanesd the test leaves behind does not keep it.
const (
	windowBase  = 42000
	windowSize  = 100
	windowCount = 69
	windowDir   = "/tmp/lanes-it-ports"
)

// nextWindow spreads a run's tests over the windows, as the counter did before
// the locks: a window a test has just released is the last one its run tries.
var nextWindow atomic.Int32

type window struct {
	lo   int
	lock *os.File
}

// claimWindow takes the first free window from the run's counter on, and waits
// while every window is held.
func claimWindow(t *testing.T) window {
	t.Helper()
	if err := os.MkdirAll(windowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	start := int(nextWindow.Add(1))
	deadline := time.Now().Add(10 * time.Minute)
	for {
		for i := range windowCount {
			lo := windowBase + (start+i)%windowCount*windowSize
			if f, ok := tryLock(t, filepath.Join(windowDir, fmt.Sprintf("%d.lock", lo))); ok {
				return window{lo: lo, lock: f}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("every port window under %s stayed held for 10 minutes", windowDir)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func tryLock(t *testing.T, path string) (*os.File, bool) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false
		}
		t.Fatalf("lock %s: %v", path, err)
	}
	return f, true
}

func (w window) release() { w.lock.Close() }

// holdWindowLock serves TestWindowLockDiesWithItsHolder: the test binary,
// re-run with LANES_IT_HOLD, locks the file and waits to be killed.
func holdWindowLock(path string) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		panic(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		panic(err)
	}
	fmt.Println("held")
	time.Sleep(time.Hour)
}

func TestWindowLockDiesWithItsHolder(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "42000.lock")
	c := exec.Command(os.Args[0])
	c.Env = append(os.Environ(), "LANES_IT_HOLD="+path)
	out, err := c.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Process.Kill() })
	if line, err := bufio.NewReader(out).ReadString('\n'); err != nil || line != "held\n" {
		t.Fatalf("the holder did not take the lock: %q %v", line, err)
	}
	if f, ok := tryLock(t, path); ok {
		f.Close()
		t.Fatal("a window held by a live run was claimed")
	}
	_ = c.Process.Kill()
	_ = c.Wait()
	f, ok := tryLock(t, path)
	if !ok {
		t.Fatal("a killed run still holds its window")
	}
	f.Close()
}

func TestWindowsDiffer(t *testing.T) {
	t.Parallel()
	a, b := claimWindow(t), claimWindow(t)
	defer a.release()
	defer b.release()
	if a.lo == b.lo {
		t.Fatalf("two harnesses share the window %d", a.lo)
	}
}
