package pc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"syscall"
	"time"
)

// MinVersion is the oldest process-compose with everything lanes drives:
// up -D and --unix-socket (1.2.0), process list JSON states (1.5.0),
// --keep-project (1.18.0), is_disabled overrides (1.63.0) and
// ordered_shutdown in YAML (1.73.0); shutdown.parent_only and per-process
// log_location predate them all (0.60.0).
var MinVersion = Version{1, 73, 0}

const Binary = "process-compose"

type Version [3]int

func (v Version) String() string { return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2]) }

func (v Version) Less(o Version) bool {
	for i := range v {
		if v[i] != o[i] {
			return v[i] < o[i]
		}
	}
	return false
}

var versionRe = regexp.MustCompile(`v?(\d+)\.(\d+)\.(\d+)`)

func ParseVersion(s string) (Version, bool) {
	m := versionRe.FindStringSubmatch(s)
	if m == nil {
		return Version{}, false
	}
	var v Version
	for i := range v {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	return v, true
}

type VersionError struct{ Got Version }

func (e *VersionError) Error() string {
	return fmt.Sprintf("process-compose %s is older than the minimum %s — run: brew upgrade process-compose", e.Got, MinVersion)
}

func Resolve(ctx context.Context) (string, error) {
	path, err := exec.LookPath(Binary)
	if err != nil {
		return "", errors.New("process-compose is not on PATH — run: brew install f1bonacc1/tap/process-compose")
	}
	out, err := exec.CommandContext(ctx, path, "version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("process-compose version: %w: %s", err, bytes.TrimSpace(out))
	}
	v, ok := ParseVersion(string(out))
	if !ok {
		return "", fmt.Errorf("process-compose version: cannot read a version in %q", bytes.TrimSpace(out))
	}
	if v.Less(MinVersion) {
		return "", &VersionError{Got: v}
	}
	return path, nil
}

type UpSpec struct {
	Binary   string
	Worktree string
	Files    []string
	Socket   string
	LogFile  string
	Environ  []string
}

// Up returns once the server answers. The server runs in its own session, so
// the stack outlives the caller.
func Up(ctx context.Context, s UpSpec) error {
	args := []string{"up", "-D", "--keep-project", "--ordered-shutdown", "-U", "-u", s.Socket, "-L", s.LogFile}
	for _, f := range s.Files {
		args = append(args, "-f", f)
	}
	bin := s.Binary
	if bin == "" {
		bin = Binary
	}
	c := exec.CommandContext(ctx, bin, args...)
	c.Dir = s.Worktree
	c.Env = s.Environ
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := c.CombinedOutput()
	if err != nil {
		return fmt.Errorf("process-compose up: %w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

type State struct {
	Name           string `json:"name"`
	Status         string `json:"status"`
	Health         string `json:"is_ready"`
	HasHealthProbe bool   `json:"has_ready_probe"`
	Restarts       int    `json:"restarts"`
	ExitCode       int    `json:"exit_code"`
	PID            int    `json:"pid"`
	IsRunning      bool   `json:"is_running"`
}

const (
	StatusDisabled  = "Disabled"
	StatusRunning   = "Running"
	StatusCompleted = "Completed"
	StatusError     = "Error"
	StatusSkipped   = "Skipped"
	HealthReady     = "Ready"
)

type Client struct {
	Socket string
	http   *http.Client
}

func NewClient(socket string) *Client {
	return &Client{
		Socket: socket,
		http: &http.Client{Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socket)
			},
		}},
	}
}

func (c *Client) Alive(ctx context.Context) bool {
	if _, err := os.Stat(c.Socket); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return c.do(ctx, http.MethodGet, "/live", nil) == nil
}

func (c *Client) States(ctx context.Context) ([]State, error) {
	var out struct {
		Data []State `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/processes", &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

func (c *Client) Start(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodPost, "/process/start/"+url.PathEscape(name), nil)
}

func (c *Client) Restart(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodPost, "/process/restart/"+url.PathEscape(name), nil)
}

// Down asks the server to shut the project down, which sends each process its
// own shutdown signal once, then waits for the server to go away.
func (c *Client) Down(ctx context.Context) error {
	if err := c.do(ctx, http.MethodPost, "/project/stop", nil); err != nil {
		// The server holds the request while its processes shut down: past
		// the deadline it is still stopping, whatever Alive answers with the
		// spent context.
		if ctx.Err() != nil {
			return fmt.Errorf("process-compose did not stop: %w", ctx.Err())
		}
		if c.Alive(ctx) {
			return err
		}
	}
	for c.Alive(ctx) {
		select {
		case <-ctx.Done():
			return fmt.Errorf("process-compose did not stop: %w", ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, "http://pc"+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &e) == nil && e.Error != "" {
			return fmt.Errorf("process-compose %s: %s", path, e.Error)
		}
		return fmt.Errorf("process-compose %s: HTTP %d", path, resp.StatusCode)
	}
	if out != nil {
		return json.Unmarshal(body, out)
	}
	return nil
}

type Failure struct {
	Piece  string
	Reason string
}

// Evaluate reads a start in progress. A piece fails when it errors, exits
// non-zero, or dies and restarts; the stack is ready once every enabled piece
// runs (and passes its readiness probe) or has completed cleanly.
func Evaluate(states []State) (bool, *Failure) {
	ready := true
	for _, s := range states {
		switch {
		case s.Status == StatusDisabled:
		case s.Status == StatusError:
			return false, &Failure{Piece: s.Name, Reason: "failed to start"}
		case s.Status == StatusCompleted && s.ExitCode != 0:
			return false, &Failure{Piece: s.Name, Reason: fmt.Sprintf("exited with code %d", s.ExitCode)}
		case s.Status == StatusSkipped:
			return false, &Failure{Piece: s.Name, Reason: "was skipped: a dependency failed"}
		case s.Restarts > 0:
			return false, &Failure{Piece: s.Name, Reason: fmt.Sprintf("died and was restarted %d time(s)", s.Restarts)}
		case s.Status == StatusCompleted:
		case s.Status == StatusRunning && (!s.HasHealthProbe || s.Health == HealthReady):
		default:
			ready = false
		}
	}
	return ready, nil
}

var oomTail = regexp.MustCompile(`Killed: 9|OutOfMemoryError`)

func KilledForMemory(s State, logTail string) bool {
	shellReportedKill := 128 + int(syscall.SIGKILL)
	goReportedKill := -int(syscall.SIGKILL)
	return s.ExitCode == shellReportedKill || s.ExitCode == goReportedKill || oomTail.MatchString(logTail)
}
