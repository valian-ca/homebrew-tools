package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	driver "github.com/go-sql-driver/mysql"

	"github.com/valian-ca/homebrew-tools/internal/lanes/config"
	"github.com/valian-ca/homebrew-tools/internal/lanes/env"
	"github.com/valian-ca/homebrew-tools/internal/lanes/fsx"
	"github.com/valian-ca/homebrew-tools/internal/lanes/paths"
	"github.com/valian-ca/homebrew-tools/internal/lanes/prep"
	"github.com/valian-ca/homebrew-tools/internal/lanes/procinfo"
	"github.com/valian-ca/homebrew-tools/internal/lanes/shellx"
)

const (
	DefaultWakeTimeout = 2 * time.Minute
	startTimeout       = 10 * time.Minute
	dialTimeout        = 3 * time.Second
	writeTimeout       = 10 * time.Second
	readTimeout        = 2 * time.Minute
	pollInterval       = 250 * time.Millisecond
	startTailLines     = 40
	erExists           = 1007
)

var (
	ErrAsleep      = errors.New("no MySQL server listens")
	ErrUnreachable = errors.New("the MySQL server does not answer")
)

func init() {
	_ = driver.SetLogger(log.New(io.Discard, "", 0))
}

// GuardError is the refusal to touch a port held by a process lanes.yaml does
// not name: a Cloud SQL proxy there speaks MySQL and leads to production.
type GuardError struct {
	Port      int
	Process   string
	PID       int
	Listeners []string
}

func (e *GuardError) Error() string {
	if e.PID == 0 {
		return fmt.Sprintf("port %d answers, but lanes cannot see which process holds it (another user's?) — lanes will not touch it", e.Port)
	}
	return fmt.Sprintf("port %d is held by %s (PID %d), not one of the MySQL listeners lanes.yaml allows [%s] — lanes will not touch it",
		e.Port, e.Process, e.PID, strings.Join(e.Listeners, ", "))
}

type Server struct {
	Spec  config.MySQL
	Procs procinfo.Source
}

func New(spec config.MySQL) *Server { return &Server{Spec: spec, Procs: procinfo.System()} }

func (s *Server) Addr() string { return net.JoinHostPort(config.MySQLHost, strconv.Itoa(s.Spec.Port)) }

// Guard identifies the program that listens on the port, before any MySQL
// connection: it tells a sleeping server (ErrAsleep) from a foreign one.
func (s *Server) Guard() error {
	pid, ok := s.Procs.ListeningPID(s.Spec.Port)
	if !ok {
		if answers(s.Addr()) {
			return &GuardError{Port: s.Spec.Port, Listeners: s.Spec.Listeners}
		}
		return fmt.Errorf("%w on %s", ErrAsleep, s.Addr())
	}
	name := "an unknown process"
	if procs, err := s.Procs.Snapshot(); err == nil {
		for _, p := range procs {
			if p.PID != pid {
				continue
			}
			if p.NamedAs(s.Spec.Listeners) {
				return nil
			}
			name = p.Comm
			if len(p.Argv) > 0 {
				name = filepath.Base(p.Argv[0])
			}
		}
	}
	return &GuardError{Port: s.Spec.Port, Process: name, PID: pid, Listeners: s.Spec.Listeners}
}

func answers(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func (s *Server) open() (*sql.DB, error) {
	c := driver.NewConfig()
	c.Net = "tcp"
	c.Addr = s.Addr()
	c.User = s.Spec.User
	c.Passwd = s.Spec.Password
	c.Timeout = dialTimeout
	c.ReadTimeout = readTimeout
	c.WriteTimeout = writeTimeout
	conn, err := driver.NewConnector(c)
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(conn)
	db.SetMaxOpenConns(1)
	return db, nil
}

// Connect never wakes the server, whatever the guard finds: only Wake does.
func (s *Server) Connect(ctx context.Context) (*sql.DB, error) {
	if err := s.Guard(); err != nil {
		return nil, err
	}
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		var me *driver.MySQLError
		if errors.As(err, &me) {
			return nil, fmt.Errorf("MySQL at %s: %w", s.Addr(), err)
		}
		return nil, fmt.Errorf("%w at %s: %v", ErrUnreachable, s.Addr(), err)
	}
	return db, nil
}

type WakeOptions struct {
	// Dir is where the start command runs: the main checkout, so a server it
	// starts never keeps its data in a worktree that can disappear.
	Dir     string
	Lock    string
	Environ []string
	Timeout time.Duration
}

func WakeTimeout() time.Duration {
	if v, ok := paths.TestOverride("LANES_MYSQL_WAKE_TIMEOUT"); ok {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return DefaultWakeTimeout
}

// Wake runs the start command under a repository lock, so lanes starting
// together never run it twice at once; the second finds the server up.
func (s *Server) Wake(ctx context.Context, o WakeOptions) error {
	err := s.Guard()
	if err == nil {
		return s.awaitReady(ctx, o.Timeout)
	}
	if !errors.Is(err, ErrAsleep) {
		return err
	}
	if s.Spec.Start == "" {
		return fmt.Errorf("the MySQL server at %s is not running, and lanes.yaml declares no start command for it — start it with the project's own commands, then run lanes start again", s.Addr())
	}
	return fsx.WithLock(o.Lock, func() error {
		err := s.Guard()
		if err == nil {
			return s.awaitReady(ctx, o.Timeout)
		}
		if !errors.Is(err, ErrAsleep) {
			return err
		}
		out, err := s.runStart(ctx, o)
		if err != nil {
			return fmt.Errorf("the MySQL start command failed: %w\n--- end of its output ---\n%s", err, out)
		}
		if err := s.awaitReady(ctx, o.Timeout); err != nil {
			return fmt.Errorf("%w\n--- end of the start command's output ---\n%s", err, out)
		}
		return nil
	})
}

// runStart writes the command's output to a file, not a pipe: a server it
// daemonizes may keep the inherited descriptors open.
func (s *Server) runStart(ctx context.Context, o WakeOptions) (string, error) {
	f, err := os.CreateTemp("", "lanes-mysql-start-*.log")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	ctx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	c := shellx.Command(ctx, s.Spec.Start)
	c.Dir = o.Dir
	c.Env = o.Environ
	c.Stdout, c.Stderr = f, f
	runErr := c.Run()
	data, _ := os.ReadFile(f.Name())
	return prep.Tail(string(data), startTailLines), runErr
}

func (s *Server) awaitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for {
		db, err := s.Connect(ctx)
		if err == nil {
			return db.Close()
		}
		var ge *GuardError
		var me *driver.MySQLError
		if errors.As(err, &ge) || errors.As(err, &me) {
			return err
		}
		last = err
		if !time.Now().Before(deadline) {
			return fmt.Errorf("the MySQL server at %s did not accept connections within %s: %v", s.Addr(), timeout, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// Ensure creates the databases that are missing and calls register for each
// name, existing or new, as soon as it exists: a crash between two creations
// leaves nothing unregistered.
func (s *Server) Ensure(ctx context.Context, names []string, register func(string) error) error {
	db, err := s.Connect(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	for _, n := range names {
		var exists int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = ?", n).Scan(&exists); err != nil {
			return fmt.Errorf("MySQL: look up %s: %w", n, err)
		}
		if exists == 0 {
			if _, err := db.ExecContext(ctx, "CREATE DATABASE "+quote(n)); err != nil && !isCode(err, erExists) {
				return fmt.Errorf("MySQL: create %s: %w", n, err)
			}
		}
		if err := register(n); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) Drop(ctx context.Context, db *sql.DB, name string) error {
	_, err := db.ExecContext(ctx, "DROP DATABASE IF EXISTS "+quote(name))
	var me *driver.MySQLError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &me):
		return fmt.Errorf("MySQL: drop %s: %w", name, err)
	default:
		return fmt.Errorf("%w: drop %s: %v", ErrUnreachable, name, err)
	}
}

func (s *Server) Databases(ctx context.Context) ([]string, error) {
	db, err := s.Connect(ctx)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, "SHOW DATABASES")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func quote(name string) string { return "`" + strings.ReplaceAll(name, "`", "``") + "`" }

func isCode(err error, code uint16) bool {
	var me *driver.MySQLError
	return errors.As(err, &me) && me.Number == code
}

var laneIDPattern = regexp.MustCompile(`^[a-z0-9_]{1,24}_[0-9a-f]{6}$`)

// Owners returns every lane whose database the name can be, one per template
// that yields it with a valid lane ID: app_test_foo_abc123 is the dev base of
// lane test_foo_abc123 under app_{lane}, and the test base of foo_abc123
// under app_test_{lane}.
func Owners(dbs config.Databases, name string) []string {
	var out []string
	for _, d := range dbs {
		parts := strings.Split(d.Template, config.LanePlaceholder)
		n := len(parts) - 1
		fixed := len(d.Template) - n*len(config.LanePlaceholder)
		if n == 0 || len(name) <= fixed || (len(name)-fixed)%n != 0 || !strings.HasPrefix(name, parts[0]) {
			continue
		}
		id := name[len(parts[0]) : len(parts[0])+(len(name)-fixed)/n]
		if laneIDPattern.MatchString(id) && d.Name(id) == name && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func Names(dbs config.Databases, laneID string) []string {
	out := make([]string, len(dbs))
	for i, d := range dbs {
		out[i] = d.Name(laneID)
	}
	return out
}

func DatabaseVar(key string) string { return "LANES_MYSQL_DATABASE_" + strings.ToUpper(key) }

// Vars needs no server: lanes env and the hooks export them while it sleeps.
func Vars(spec config.MySQL, laneID string) env.Vars {
	v := env.Vars{
		{Name: "LANES_MYSQL_HOST", Value: config.MySQLHost},
		{Name: "LANES_MYSQL_PORT", Value: strconv.Itoa(spec.Port)},
		{Name: "LANES_MYSQL_USER", Value: spec.User},
		{Name: "LANES_MYSQL_PASSWORD", Value: spec.Password},
	}
	for _, d := range spec.Databases {
		v = append(v, env.Var{Name: DatabaseVar(d.Key), Value: d.Name(laneID)})
	}
	return v
}
