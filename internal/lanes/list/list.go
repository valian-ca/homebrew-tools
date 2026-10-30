package list

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/lanes/config"
	"github.com/valian-ca/homebrew-tools/internal/lanes/env"
	"github.com/valian-ca/homebrew-tools/internal/lanes/gitx"
	"github.com/valian-ca/homebrew-tools/internal/lanes/lane"
	"github.com/valian-ca/homebrew-tools/internal/lanes/paths"
	"github.com/valian-ca/homebrew-tools/internal/lanes/pc"
	"github.com/valian-ca/homebrew-tools/internal/lanes/ports"
	"github.com/valian-ca/homebrew-tools/internal/lanes/procinfo"
	"github.com/valian-ca/homebrew-tools/internal/lanes/proto"
	"github.com/valian-ca/homebrew-tools/internal/lanes/registry"
	"github.com/valian-ca/homebrew-tools/internal/lanes/tmpdir"
	"github.com/valian-ca/homebrew-tools/internal/lanes/workspace"
)

const (
	StateRunning = "running"
	StateStopped = "stopped"

	SessionActive  = "active"
	SessionNone    = "none"
	SessionGrace   = "grace"
	SessionUnknown = "unknown"
)

type Lane struct {
	Project      string         `json:"project"`
	Repo         string         `json:"repo"`
	Worktree     string         `json:"worktree"`
	Lane         string         `json:"lane"`
	Branch       string         `json:"branch"`
	Host         string         `json:"host"`
	Block        Block          `json:"block"`
	Ports        map[string]int `json:"ports"`
	State        string         `json:"state"`
	Session      string         `json:"session"`
	GraceLeft    string         `json:"graceLeft,omitempty"`
	LastActivity *time.Time     `json:"lastActivity"`
	Tmpdir       string         `json:"tmpdir"`
	Logs         string         `json:"logs"`
	Pieces       []Piece        `json:"pieces"`
	Resources    []Resource     `json:"resources"`
	RAM          int64          `json:"ram"`
	RAMThreshold int64          `json:"ramThreshold"`
	RAMAlert     bool           `json:"ramAlert"`
}

type Block struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type Resource struct {
	Adapter  string `json:"adapter"`
	Kind     string `json:"kind"`
	Resource string `json:"resource"`
}

type Piece struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	PID    int    `json:"pid,omitempty"`
	RAM    int64  `json:"ram"`
}

type Report struct {
	DaemonRunning bool   `json:"daemonRunning"`
	Lanes         []Lane `json:"lanes"`
}

// stacks is lanesd's view, nil when lanesd is not running.
func Build(ctx context.Context, stacks []proto.StackStatus, procs procinfo.Source) (Report, error) {
	t, err := ports.Load()
	if err != nil {
		return Report{}, err
	}
	byAdmin := map[string]proto.StackStatus{}
	for _, s := range stacks {
		byAdmin[s.AdminDir] = s
	}
	snapshot, _ := procs.Snapshot()

	r := Report{DaemonRunning: stacks != nil, Lanes: []Lane{}}
	for _, e := range t.Entries {
		if gone, err := gitx.Gone(e.AdminDir); gone || err != nil {
			continue
		}
		r.Lanes = append(r.Lanes, describe(ctx, e, byAdmin, stacks != nil, snapshot))
	}
	slices.SortFunc(r.Lanes, func(a, b Lane) int { return strings.Compare(a.Worktree, b.Worktree) })
	return r, nil
}

func describe(ctx context.Context, e ports.Entry, stacks map[string]proto.StackStatus, daemonUp bool, snapshot []procinfo.Proc) Lane {
	tmp := tmpdir.Path(e.CommonDir, e.AdminDir)
	l := Lane{
		Project:      e.Project,
		Repo:         filepath.Dir(e.CommonDir),
		Worktree:     e.Worktree,
		Block:        Block{Start: e.Start, End: e.End()},
		Ports:        map[string]int{},
		State:        StateStopped,
		Session:      SessionUnknown,
		Host:         env.DefaultHost,
		Tmpdir:       tmp,
		Logs:         paths.Logs(tmp),
		Pieces:       []Piece{},
		Resources:    []Resource{},
		RAMThreshold: int64(config.DefaultRAMAlert),
	}
	if rec, err := lane.Load(e.AdminDir); err == nil && rec != nil {
		l.Lane, l.Branch = rec.ID, rec.Branch
		if rec.Host != "" {
			l.Host = rec.Host
		}
		l.Resources = resources(e, rec.ID)
	}
	if cfg, err := config.Load(e.Worktree); err == nil {
		for i, n := range cfg.Ports.Names {
			l.Ports[env.PortVar(n)] = e.Start + i
		}
		l.RAMThreshold = int64(cfg.RAMAlert)
	}
	if at := workspace.LastActivity(e.AdminDir); !at.IsZero() {
		l.LastActivity = &at
	}

	c := pc.NewClient(paths.PCSocket(tmp))
	if c.Alive(ctx) {
		l.State = StateRunning
		if states, err := c.States(ctx); err == nil {
			for _, s := range states {
				p := Piece{Name: s.Name, Status: s.Status}
				if s.IsRunning && s.PID > 0 {
					p.PID = s.PID
					for _, d := range procinfo.Descendants(snapshot, s.PID) {
						p.RAM += int64(d.RSS)
					}
				}
				l.RAM += p.RAM
				l.Pieces = append(l.Pieces, p)
			}
		}
	}
	l.RAMAlert = l.State == StateRunning && l.RAM > l.RAMThreshold

	if daemonUp {
		s, ok := stacks[e.AdminDir]
		switch {
		case l.State != StateRunning:
			l.Session = SessionNone
		case ok && s.Sessions > 0:
			l.Session = SessionActive
		case ok && s.GraceUntil != nil:
			l.Session = SessionGrace
			l.GraceLeft = time.Until(*s.GraceUntil).Round(time.Second).String()
		default:
			l.Session = SessionNone
		}
	}
	return l
}

func resources(e ports.Entry, laneID string) []Resource {
	out := []Resource{}
	entries, err := registry.Load(e.CommonDir)
	if err != nil {
		return out
	}
	for _, en := range entries {
		if en.AdminDir == e.AdminDir && en.Lane == laneID && en.Kind != registry.KindTmpdir {
			out = append(out, Resource{Adapter: en.Adapter, Kind: en.Kind, Resource: en.Resource})
		}
	}
	return out
}

func WriteText(w io.Writer, r Report) {
	if !r.DaemonRunning {
		fmt.Fprintln(w, "lanesd is not running — session states are unknown (brew services start lanes)")
	}
	if len(r.Lanes) == 0 {
		fmt.Fprintln(w, "no lanes")
		return
	}
	for i, l := range r.Lanes {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "%s  (%s)\n", l.Worktree, l.Project)
		fmt.Fprintf(w, "  lane      %s  (branch %s)\n", orDash(l.Lane), orDash(l.Branch))
		fmt.Fprintf(w, "  host      %s\n", l.Host)
		fmt.Fprintf(w, "  ports     %d-%d%s\n", l.Block.Start, l.Block.End, namedPorts(l.Ports))
		session := l.Session
		if l.Session == SessionGrace {
			session += " (" + l.GraceLeft + " left)"
		}
		fmt.Fprintf(w, "  state     %s · session %s\n", l.State, session)
		if l.State == StateRunning {
			alert := ""
			if l.RAMAlert {
				alert = fmt.Sprintf("  ⚠ above the %s alert threshold", config.ByteSize(l.RAMThreshold))
			}
			fmt.Fprintf(w, "  ram       %s%s\n", mb(l.RAM), alert)
			for _, p := range l.Pieces {
				fmt.Fprintf(w, "    %-16s %-10s %s\n", p.Name, p.Status, mb(p.RAM))
			}
		}
		activity := "-"
		if l.LastActivity != nil {
			activity = l.LastActivity.Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(w, "  activity  %s\n", activity)
		fmt.Fprintf(w, "  logs      %s\n", l.Logs)
	}
}

func namedPorts(m map[string]int) string {
	if len(m) == 0 {
		return ""
	}
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	slices.SortFunc(names, func(a, b string) int { return m[a] - m[b] })
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, "  %s=%d", n, m[n])
	}
	return b.String()
}

func mb(n int64) string {
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
