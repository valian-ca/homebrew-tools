package shellx

import (
	"context"
	"os/exec"
	"syscall"
	"time"
)

const pipeGrace = 5 * time.Second

// Command runs a project's shell command in its own process group. A
// cancelled command takes its whole group down, and the wait stops after
// pipeGrace once the shell is gone: a server the command left running in the
// background keeps the output pipes open and would otherwise hold Wait forever.
func Command(ctx context.Context, command string) *exec.Cmd {
	c := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error { return syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }
	c.WaitDelay = pipeGrace
	return c
}
