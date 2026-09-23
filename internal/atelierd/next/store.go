package next

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	oklogulid "github.com/oklog/ulid/v2"
	"golang.org/x/sys/unix"
)

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var ticketPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]*-[1-9][0-9]*$`)
var hashPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

type Store struct {
	root string
	now  func() time.Time
}

func Open() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		return nil, err
	}
	return &Store{root: filepath.Join(home, ".atelier-next"), now: time.Now}, nil
}

func realDirectory(path string, create bool) error {
	if create {
		if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("next: %s is not a real directory", path)
	}
	return os.Chmod(path, 0o700)
}

func (s *Store) locked(ctx context.Context, fn func() error) error {
	if err := realDirectory(s.root, true); err != nil {
		return err
	}
	fd, err := unix.Open(filepath.Join(s.root, ".lock"), unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0o600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), "next-lock")
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("next: lock is not a regular file")
	}
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	wait, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for {
		if err := wait.Err(); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return ErrBusy
		}
		err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EINTR) {
			return err
		}
		select {
		case <-wait.Done():
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer func() { _ = unix.Flock(fd, unix.LOCK_UN) }()
	if err := realDirectory(filepath.Join(s.root, "campaigns"), true); err != nil {
		return err
	}
	return fn()
}

func readFile(path string) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("next: %s is not a regular file", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxFileBytes {
		return nil, fmt.Errorf("%w: file exceeds %d bytes", ErrInvalid, MaxFileBytes)
	}
	return data, nil
}

func DecodeFile(path string, target any) error {
	data, err := readFile(path)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := decode(data, target); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return nil
}

func decode(data []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("expected exactly one JSON value")
	}
	return nil
}

func validID(id string) bool {
	parsed, err := oklogulid.ParseStrict(id)
	return err == nil && parsed.String() == id
}

func (s *Store) path(id string) string { return filepath.Join(s.root, "campaigns", id+".json") }

func (s *Store) load(id string) (*Campaign, error) {
	if !validID(id) {
		return nil, ErrNotFound
	}
	data, err := readFile(s.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var header struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return nil, fmt.Errorf("%w: corrupt campaign JSON: %v", ErrInvalid, err)
	}
	if header.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("%w: campaign schema %d is unsupported (expected %d); preserve the state and resolve with compatible tooling, no automatic migration", ErrInvalid, header.SchemaVersion, SchemaVersion)
	}
	var c Campaign
	if err := decode(data, &c); err != nil {
		return nil, fmt.Errorf("%w: corrupt campaign: %v", ErrInvalid, err)
	}
	if err := validateState(&c, id); err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) save(c *Campaign) error {
	if err := validateState(c, c.ID); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if len(data)+1 > MaxFileBytes {
		return fmt.Errorf("%w: campaign capacity reached", ErrInvalid)
	}
	path := s.path(c.ID)
	info, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("next: state is not a regular file")
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".next-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	defer tmp.Close()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (s *Store) all() ([]*Campaign, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, "campaigns"))
	if err != nil {
		return nil, err
	}
	var result []*Campaign
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		if !validID(id) {
			return nil, fmt.Errorf("next: invalid campaign filename %q", e.Name())
		}
		c, err := s.load(id)
		if err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, nil
}

func (s *Store) Status(ctx context.Context, id string) (*Campaign, error) {
	var c *Campaign
	err := s.locked(ctx, func() error {
		var err error
		c, err = s.load(id)
		return err
	})
	return c, err
}

func (s *Store) Find(ctx context.Context, ticket, repository string) (string, error) {
	if !ticketPattern.MatchString(ticket) {
		return "", fmt.Errorf("%w: exact root ticket required", ErrInvalid)
	}
	var found string
	err := s.locked(ctx, func() error {
		all, err := s.all()
		if err != nil {
			return err
		}
		for _, c := range all {
			if c.Root.Identifier != ticket || (repository != "" && c.Checkout.Repository != repository) {
				continue
			}
			if found != "" {
				return ErrAmbiguous
			}
			found = c.ID
		}
		if found == "" {
			return ErrNotFound
		}
		return nil
	})
	return found, err
}

func fingerprint(r Request) string {
	data, _ := json.Marshal(r)
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func (s *Store) Apply(ctx context.Context, r Request) (Receipt, error) {
	var receipt Receipt
	if !keyPattern.MatchString(r.OperationID) || !keyPattern.MatchString(r.Session) {
		return receipt, fmt.Errorf("%w: operation and session must be 1–128 safe identifier characters", ErrInvalid)
	}
	err := s.locked(ctx, func() error {
		checkout, head, err := inspectCheckout(ctx, r.CWD)
		if err != nil {
			return err
		}
		r.CWD = checkout.Worktree
		digest := fingerprint(r)
		var c *Campaign
		if r.Action == "start" {
			c, err = s.start(ctx, r, checkout, head, digest)
		} else {
			c, err = s.load(r.CampaignID)
		}
		if err != nil {
			return err
		}
		if checkout != c.Checkout {
			return ErrCheckout
		}
		if op, ok := c.Operations[r.OperationID]; ok {
			if op.Digest != digest {
				return fmt.Errorf("%w: operation ID reused with different arguments", ErrConflict)
			}
			receipt = op.Receipt
			return nil
		}
		if len(c.Operations) >= MaxOperations {
			return fmt.Errorf("%w: operation limit reached", ErrInvalid)
		}
		now := s.now().UTC()
		receipt = Receipt{CampaignID: c.ID, OperationID: r.OperationID, Revision: c.Revision + 1}
		if err := s.mutate(ctx, c, r, head, now, &receipt); err != nil {
			return err
		}
		c.Revision++
		c.Operations[r.OperationID] = Operation{Digest: digest, Receipt: receipt}
		return s.save(c)
	})
	if err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}
