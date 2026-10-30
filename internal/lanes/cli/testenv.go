package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/valian-ca/homebrew-tools/internal/lanes/daemon"
	"github.com/valian-ca/homebrew-tools/internal/lanes/firebase"
	"github.com/valian-ca/homebrew-tools/internal/lanes/paths"
	"github.com/valian-ca/homebrew-tools/internal/lanes/procinfo"
	"github.com/valian-ca/homebrew-tools/internal/lanes/workspace"
)

const orphanPatience = 5 * time.Second

type exitCodeError struct{ code int }

func (e *exitCodeError) Error() string { return fmt.Sprintf("the command exited with code %d", e.code) }

func (e *exitCodeError) ExitCode() int { return e.code }

func newTestEnvCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "test-env -- <command> [args...]",
		Short: "Run a command against this lane's test series of emulators",
		Long: `Run a command in the foreground with the lane's variables pointed at its test
series: every firebase PORT_<NAME>, the SDK variables and LANES_FIREBASE_CONFIG
name the test_* ports and firebase.lanes.test.json, TMPDIR is $TMPDIR/test,
and the JVM heap and function load caps apply as in the stack.
LANES_FIREBASE_JVM_HEAP and LANES_FIREBASE_FUNCTIONS_TIMEOUT, when set, replace
the project's caps.

The command starts the emulators itself, for instance with
  firebase emulators:exec --config "$LANES_FIREBASE_CONFIG" "<suite>"
The test config lists only test.only and the emulators Firebase starts with
them, without the UI. lanes imports and exports nothing: the lane's data
directory is never touched. It works whether the stack runs or not, and never
touches the stack. Before and after the command, lanes ends what a test-env
killed outright left listening on the test ports.

One test-env runs at a time in a lane: a second one refuses, naming the PID of
the first. On a main checkout, outside a repository or without lanes.yaml, the
command runs as is. The exit status is the command's.`,
		Example: `  lanes test-env -- npm run test:emu
  lanes test-env -- firebase emulators:exec --config "$LANES_FIREBASE_CONFIG" "npm test"`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := cwd()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if _, _, err := workspace.Locate(ctx, dir); workspace.IsQuiet(err) {
				return runForeground(args, os.Environ())
			} else if err != nil {
				return err
			}
			w, err := settle(ctx, dir, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if w.EnvErr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "lanes: %v — the env: variables are left out\n", w.EnvErr)
			}
			testTmp := filepath.Join(w.Tmpdir, "test")
			environ, held, err := testEnviron(ctx, w, testTmp)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(testTmp, paths.DirMode); err != nil {
				return err
			}
			release, err := lockTestEnv(filepath.Join(testTmp, "lock"))
			if err != nil {
				return err
			}
			defer release()
			clearTestOrphans(ctx, w, held, cmd.ErrOrStderr())
			err = runForeground(args, environ)
			if fb := w.Cfg.Firebase(); fb != nil {
				firebase.SweepOrphans(procinfo.System(), w.WT.Root, fb.ExportTimeout.Std()+firebase.ExportGrace, cmd.ErrOrStderr())
			}
			clearTestOrphans(ctx, w, held, cmd.ErrOrStderr())
			return err
		},
	}
}

// testEnviron also returns the test series' ports, the ones a killed
// test-env's emulators keep.
func testEnviron(ctx context.Context, w *workspace.Workspace, testTmp string) ([]string, []int, error) {
	if err := prepare(ctx, w, os.Stderr); err != nil {
		return nil, nil, err
	}
	environ := w.TestVars().Environ(os.Environ())
	fb := w.Cfg.Firebase()
	if fb == nil {
		return environ, nil, nil
	}
	b, err := firebase.LoadBase(w.WT.Root, fb)
	if err != nil {
		return nil, nil, err
	}
	required := firebase.NewPlan(b, fb, firebase.Test).RequiredPorts()
	if missing := firebase.Missing(required, w.Cfg.Ports.Names); len(missing) > 0 {
		return nil, nil, firebase.MissingError(missing)
	}
	if err := firebase.CheckSocket(testTmp); err != nil {
		return nil, nil, err
	}
	if _, err := firebase.Binary(w.WT.Root, fb); err != nil {
		return nil, nil, err
	}
	caps, err := firebase.Caps(fb, environ)
	if err != nil {
		return nil, nil, err
	}
	byName := firebase.Ports(w.Cfg.Ports.Names, w.Block.Start)
	held := make([]int, 0, len(required))
	for _, n := range required {
		held = append(held, byName[n])
	}
	return append(environ, caps...), held, nil
}

// clearTestOrphans ends what a test-env killed outright left listening on the
// test ports — the lock proves no other test-env of the lane is alive.
func clearTestOrphans(ctx context.Context, w *workspace.Workspace, held []int, log io.Writer) {
	src := procinfo.System()
	var holders []int
	for _, p := range held {
		if pid, ok := src.ListeningPID(p); ok {
			holders = append(holders, pid)
		}
	}
	if len(holders) == 0 {
		return
	}
	procs, err := src.Snapshot()
	if err != nil {
		return
	}
	keep := daemon.Keep{Sessions: w.Cfg.Sessions, Shared: w.Cfg.Shared, Spare: w.Cfg.Spare}
	targets := daemon.OrphanHolders(procs, w.WT.Root, keep, os.Getpid(), holders)
	if len(targets) == 0 {
		return
	}
	fmt.Fprintf(log, "lanes: ending %d process(es) a killed test-env left on the test ports\n", len(targets))
	daemon.End(ctx, src, syscall.Kill, procs, targets, orphanPatience)
}

// lockTestEnv holds an flock for the life of the run: the kernel drops it
// with the process, so a killed test-env never blocks the next one.
func lockTestEnv(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, paths.FileMode)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		holder, _ := os.ReadFile(path)
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("lanes test-env is already running in this lane (PID %s)", strings.TrimSpace(string(holder)))
		}
		return nil, err
	}
	if err := f.Truncate(0); err != nil {
		_ = f.Close()
		return nil, err
	}
	if _, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = f.Truncate(0)
		_ = f.Close()
	}, nil
}

// runForeground leaves SIGINT to the terminal, which sends it to the whole
// foreground group; a second one would make firebase-tools skip its cleanup.
func runForeground(args, environ []string) error {
	c := exec.Command(args[0], args[1:]...)
	c.Env = environ
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Start(); err != nil {
		return err
	}
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	for {
		select {
		case s := <-sigs:
			_ = c.Process.Signal(s)
		case err := <-done:
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code := ee.ExitCode()
				if code < 0 {
					code = 128 + int(ee.Sys().(syscall.WaitStatus).Signal())
				}
				return &exitCodeError{code}
			}
			return err
		}
	}
}
