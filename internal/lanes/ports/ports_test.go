package ports

import (
	"net"
	"strconv"
	"testing"

	"github.com/valian-ca/homebrew-tools/internal/lanes/testutil"
)

func entry(admin string, size int) Entry {
	return Entry{CommonDir: "/repo/.git", AdminDir: admin, Worktree: "/wt/" + admin, Project: "demo", Size: size}
}

func TestEnsureAllocatesDisjointBlocksAndKeepsThem(t *testing.T) {
	testutil.StateDir(t)
	t.Setenv("LANES_PORT_RANGE", "31000-31999")

	a, err := Ensure(entry("a", 10))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Ensure(entry("b", 20))
	if err != nil {
		t.Fatal(err)
	}
	if a.Start < 31000 || b.End() > 31999 {
		t.Fatalf("blocks outside the range: %+v %+v", a, b)
	}
	if a.Start <= b.End() && b.Start <= a.End() {
		t.Fatalf("blocks overlap: %+v %+v", a, b)
	}
	again, err := Ensure(entry("a", 10))
	if err != nil || again.Start != a.Start {
		t.Fatalf("second Ensure moved the block: %+v, %v", again, err)
	}
	if got, ok, _ := Find("b"); !ok || got.Start != b.Start {
		t.Fatalf("Find(b) = %+v, %v", got, ok)
	}
}

func TestEnsureSkipsPortsInUse(t *testing.T) {
	testutil.StateDir(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	held := ln.Addr().(*net.TCPAddr).Port
	t.Setenv("LANES_PORT_RANGE", strconv.Itoa(held)+"-"+strconv.Itoa(held+50))

	e, err := Ensure(entry("a", 5))
	if err != nil {
		t.Fatal(err)
	}
	if e.Contains(held) {
		t.Fatalf("block %+v contains the held port %d", e, held)
	}
}

func TestEnsureReportsExhaustion(t *testing.T) {
	testutil.StateDir(t)
	t.Setenv("LANES_PORT_RANGE", "32000-32009")
	if _, err := Ensure(entry("a", 10)); err != nil {
		t.Fatal(err)
	}
	if _, err := Ensure(entry("b", 1)); err == nil {
		t.Fatal("allocated past the end of the range")
	}
}

func TestRemoveFreesTheBlock(t *testing.T) {
	testutil.StateDir(t)
	t.Setenv("LANES_PORT_RANGE", "33000-33009")
	a, _ := Ensure(entry("a", 10))
	if err := Remove("a"); err != nil {
		t.Fatal(err)
	}
	b, err := Ensure(entry("b", 10))
	if err != nil || b.Start != a.Start {
		t.Fatalf("freed block not reused: %+v, %v", b, err)
	}
}

func TestRangeOverrideNeedsTestMode(t *testing.T) {
	t.Setenv("LANES_TEST", "")
	t.Setenv("LANES_PORT_RANGE", "1-2")
	if lo, hi := Range(); lo != RangeLow || hi != RangeHigh {
		t.Fatalf("Range() = %d-%d outside test mode", lo, hi)
	}
}

func TestBusySeesWildcardListeners(t *testing.T) {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if !Busy(ln.Addr().(*net.TCPAddr).Port) {
		t.Fatal("Busy missed a wildcard listener")
	}
}
