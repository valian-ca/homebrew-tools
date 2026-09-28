package updater

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// printOutput mimics the head of `launchctl print gui/<uid>/<label>`.
func printOutput(pid string) string {
	out := "gui/501/sh.brew.atelierd = {\n\tactive count = 1\n\tpath = /Users/x/Library/LaunchAgents/sh.brew.atelierd.plist\n\ttype = LaunchAgent\n\tstate = running\n\n\tprogram = /opt/homebrew/opt/atelierd/bin/atelierd\n"
	if pid != "" {
		out += "\tpid = " + pid + "\n"
	}
	return out + "\truns = 1\n\tlast exit code = (never exited)\n}\n"
}

type printFake map[string]string // target -> output; missing target = not loaded

func (p printFake) run(_ context.Context, name string, args ...string) ([]byte, error) {
	if name != launchctlPath || len(args) != 2 || args[0] != "print" {
		return nil, errors.New("unexpected command")
	}
	out, ok := p[args[1]]
	if !ok {
		return []byte("Could not find service"), errors.New("exit status 113")
	}
	return []byte(out), nil
}

func TestParsePID(t *testing.T) {
	t.Parallel()
	if pid, ok := parsePID(printOutput("52401")); !ok || pid != 52401 {
		t.Fatalf("parsePID = %d, %v; want 52401, true", pid, ok)
	}
	if _, ok := parsePID(printOutput("")); ok {
		t.Fatal("a stopped job has no pid line")
	}
}

func TestJobResolution(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		loaded  printFake
		want    string
		wantErr bool
	}{
		{
			name:   "Homebrew 6 label",
			loaded: printFake{"gui/501/sh.brew.atelierd": printOutput("4242")},
			want:   "gui/501/sh.brew.atelierd",
		},
		{
			name:   "legacy label on an older Homebrew",
			loaded: printFake{"gui/501/homebrew.mxcl.atelierd": printOutput("4242")},
			want:   "gui/501/homebrew.mxcl.atelierd",
		},
		{
			// Foreground `atelierd run` while the service also runs: the job is
			// someone else's process, so kickstarting it would be wrong.
			name:    "job runs another pid",
			loaded:  printFake{"gui/501/sh.brew.atelierd": printOutput("999")},
			wantErr: true,
		},
		{
			name:    "job loaded but not running",
			loaded:  printFake{"gui/501/sh.brew.atelierd": printOutput("")},
			wantErr: true,
		},
		{
			name:    "not launchd-managed",
			loaded:  printFake{},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &Relauncher{run: tc.loaded.run, uid: 501, pid: 4242, labels: ServiceLabels}
			got, err := r.Job(context.Background())
			if tc.wantErr {
				if !errors.Is(err, ErrNotLaunchdManaged) {
					t.Fatalf("err = %v, want ErrNotLaunchdManaged", err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("Job() = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestRelaunchKickstartsOwnJob(t *testing.T) {
	t.Parallel()
	var spawned []string
	r := &Relauncher{
		run:    printFake{"gui/501/sh.brew.atelierd": printOutput("4242")}.run,
		spawn:  func(name string, args ...string) error { spawned = append([]string{name}, args...); return nil },
		uid:    501,
		pid:    4242,
		labels: ServiceLabels,
	}
	target, err := r.Relaunch(context.Background())
	if err != nil || target != "gui/501/sh.brew.atelierd" {
		t.Fatalf("Relaunch() = %q, %v", target, err)
	}
	want := []string{launchctlPath, "kickstart", "-k", "gui/501/sh.brew.atelierd"}
	if !reflect.DeepEqual(spawned, want) {
		t.Fatalf("spawned %v, want %v", spawned, want)
	}
}

func TestRelaunchDoesNotSpawnWhenUnmanaged(t *testing.T) {
	t.Parallel()
	spawned := false
	r := &Relauncher{
		run:    printFake{}.run,
		spawn:  func(string, ...string) error { spawned = true; return nil },
		uid:    501,
		pid:    4242,
		labels: ServiceLabels,
	}
	if _, err := r.Relaunch(context.Background()); err == nil {
		t.Fatal("expected an error when no job owns this process")
	}
	if spawned {
		t.Fatal("must never kickstart a job this process does not own")
	}
}

func TestRelaunchSurfacesSpawnFailure(t *testing.T) {
	t.Parallel()
	r := &Relauncher{
		run:    printFake{"gui/501/sh.brew.atelierd": printOutput("4242")}.run,
		spawn:  func(string, ...string) error { return errors.New("no launchctl") },
		uid:    501,
		pid:    4242,
		labels: ServiceLabels,
	}
	if _, err := r.Relaunch(context.Background()); err == nil {
		t.Fatal("expected spawn failure to surface so the caller falls back to exiting")
	}
}
