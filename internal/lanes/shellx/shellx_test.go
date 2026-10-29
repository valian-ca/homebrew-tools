package shellx

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestCommandReturnsDespiteABackgroundChildHoldingItsOutput(t *testing.T) {
	c := Command(context.Background(), "sleep 30 & echo started")
	var out bytes.Buffer
	c.Stdout = &out
	began := time.Now()
	_ = c.Run()
	if time.Since(began) > pipeGrace+3*time.Second {
		t.Fatalf("Run waited %s for a background child", time.Since(began))
	}
	if !strings.Contains(out.String(), "started") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestCancelKillsTheWholeGroup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	c := Command(ctx, "sleep 30; echo never")
	var out bytes.Buffer
	c.Stdout = &out
	began := time.Now()
	if err := c.Run(); err == nil {
		t.Fatal("a cancelled command succeeded")
	}
	if time.Since(began) > 3*time.Second || out.Len() != 0 {
		t.Fatalf("cancel took %s, output %q", time.Since(began), out.String())
	}
}
