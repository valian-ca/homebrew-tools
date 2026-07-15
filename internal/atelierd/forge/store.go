package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
	oklogulid "github.com/oklog/ulid/v2"
	"golang.org/x/sys/unix"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/events"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/outbox"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/paths"
	atelierulid "github.com/valian-ca/homebrew-tools/internal/atelierd/ulid"
)

const retention = 14 * 24 * time.Hour

const (
	runLockDeadline      = 250 * time.Millisecond
	runLockRetryInterval = 10 * time.Millisecond
)

var runLockWait = runLockDeadline

func ensureDir(path string) error {
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	parts := strings.Split(filepath.Clean(path), string(filepath.Separator))
	current := string(filepath.Separator)
	for _, part := range parts {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			if err := os.Mkdir(current, paths.DirMode); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			info, statErr = os.Lstat(current)
		}
		if statErr != nil {
			return statErr
		}
		// HOME (and its ancestors) may legitimately contain symlinks on macOS
		// (/var and /tmp do).  The forge boundary itself must not: once we
		// reach ~/.atelier, every component is checked without following links.
		if (info.Mode()&os.ModeSymlink != 0 && part == ".atelier") ||
			(info.Mode()&os.ModeSymlink == 0 && !info.IsDir()) {
			return fmt.Errorf("forge path component %q is not a real directory", current)
		}
	}
	return os.Chmod(path, paths.DirMode)
}

func openNoFollow(path string, flags int, mode uint32) (*os.File, error) {
	fd, err := unix.Open(path, flags|unix.O_NOFOLLOW, mode)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func readNoFollow(path string, maxBytes int64) ([]byte, error) {
	file, err := openNoFollow(path, unix.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("file exceeds %d bytes", maxBytes)
	}
	return data, nil
}

func rejectSymlink(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("forge file %q is not a regular file", path)
	}
	return nil
}

func createDir(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Mkdir(path, paths.DirMode); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("forge directory %q is not a real directory", path)
	}
	return nil
}

func validateRunID(runID string) error {
	parsed, err := oklogulid.ParseStrict(runID)
	if err != nil || parsed.String() != runID {
		return fmt.Errorf("%w: %s", ErrUnknownRun, runID)
	}
	return nil
}

func withRunLock(runID string, fn func() error) error {
	return withRunLockContext(context.Background(), runID, fn)
}

func withRunLockContext(ctx context.Context, runID string, fn func() error) error {
	if err := validateRunID(runID); err != nil {
		return err
	}
	info, err := os.Lstat(paths.ForgeRun(runID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %s", ErrUnknownRun, runID)
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: %s", ErrUnknownRun, runID)
	}
	lock, err := openNoFollow(paths.ForgeRunLock(runID), unix.O_RDWR|unix.O_CREAT, uint32(paths.FileMode.Perm()))
	if err != nil {
		return fmt.Errorf("open forge run lock: %w", err)
	}
	defer lock.Close()
	flockLock := flock.New(lock.Name())
	defer flockLock.Close()
	lockCtx, cancel := context.WithTimeout(ctx, runLockWait)
	defer cancel()
	ok, err := flockLock.TryLockContext(lockCtx, runLockRetryInterval)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return ErrRunBusy
		}
		return fmt.Errorf("acquire forge run lock: %w", err)
	}
	if !ok {
		return ErrRunBusy
	}
	defer func() { _ = flockLock.Unlock() }()
	if err := unix.Fchmod(int(lock.Fd()), uint32(paths.FileMode.Perm())); err != nil {
		return err
	}
	operationErr := fn()
	// Telemetry is best effort: state transitions above are the operation's
	// durable result. Re-read the state so every forge command gets a chance to
	// drain events left by an unavailable outbox, without exposing that failure
	// as a post-commit command error.
	_ = flushPending(runID)
	return operationErr
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func readRun(runID string) (*runState, error) {
	data, err := readNoFollow(paths.ForgeRunState(runID), MaxRunFileBytes)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrUnknownRun, runID)
		}
		return nil, err
	}
	var state runState
	if err := decodeStrict(data, &state); err != nil {
		return nil, fmt.Errorf("parse run state: %w", err)
	}
	if state.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("run schema version %d is unsupported; expected %d", state.SchemaVersion, SchemaVersion)
	}
	if state.RunID != runID {
		return nil, fmt.Errorf("run state id %q does not match directory %q", state.RunID, runID)
	}
	if len(state.Ticket) > MaxTextBytes || len(state.Session) > MaxIDBytes || len(state.OpenPass) > MaxIDBytes || len(state.Refs.Report) > MaxTextBytes || len(state.Refs.Testplan) > MaxTextBytes {
		return nil, errors.New("run state string exceeds v1 limit")
	}
	if len(state.Waves) > MaxWaves || len(state.Passes) > MaxPasses || len(state.PendingEvents) > MaxPendingEvents {
		return nil, errors.New("run state collection exceeds v1 limit")
	}
	if state.NextReview < 1 || state.NextReview > MaxAuxiliaryPasses+1 || state.NextRepair < 1 || state.NextRepair > MaxAuxiliaryPasses+1 {
		return nil, errors.New("run state auxiliary pass counter exceeds v1 limit")
	}
	return &state, nil
}

func readCampaign(runID string) (*Campaign, error) {
	campaign, err := readCampaignIfPresent(runID)
	if err != nil {
		return nil, err
	}
	if campaign == nil {
		return nil, ErrCampaignInvalid
	}
	return campaign, nil
}

func readCampaignIfPresent(runID string) (*Campaign, error) {
	data, err := readNoFollow(paths.ForgeCampaign(runID), MaxCampaignFileBytes)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: read campaign: %v", ErrCampaignInvalid, err)
	}
	var campaign Campaign
	if err := decodeStrict(data, &campaign); err != nil {
		return nil, fmt.Errorf("%w: parse campaign: %v", ErrCampaignInvalid, err)
	}
	if campaign.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("%w: schema version %d is unsupported; expected %d", ErrCampaignInvalid, campaign.SchemaVersion, SchemaVersion)
	}
	if err := validateCampaign(&campaign); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCampaignInvalid, err)
	}
	return &campaign, nil
}

func readLedger(runID string) (*ledger, error) {
	data, err := readNoFollow(paths.ForgeLedger(runID), MaxLedgerFileBytes)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &ledger{SchemaVersion: SchemaVersion, Passes: []ledgerPass{}}, nil
		}
		return nil, err
	}
	var value ledger
	if err := decodeStrict(data, &value); err != nil {
		return nil, fmt.Errorf("parse ledger: %w", err)
	}
	if value.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("ledger schema version %d is unsupported; expected %d", value.SchemaVersion, SchemaVersion)
	}
	if err := validateLedger(&value); err != nil {
		return nil, fmt.Errorf("invalid ledger: %w", err)
	}
	return &value, nil
}

func validatePersistedData(state *runState, campaign *Campaign, value *ledger) error {
	allocated := make(map[string]pass, len(state.Passes))
	for _, allocatedPass := range state.Passes {
		allocated[allocatedPass.ID] = allocatedPass
	}
	knownScenarios := make(map[string]struct{})
	for _, axis := range campaign.Axes {
		for _, scenario := range axis.Scenarios {
			knownScenarios[outcomeKey(axis.Title, scenario.Title)] = struct{}{}
		}
	}
	for _, recorded := range value.Passes {
		allocatedPass, ok := allocated[recorded.PassID]
		if !ok {
			return fmt.Errorf("persisted ledger pass %q is not allocated in run state", recorded.PassID)
		}
		if recorded.Kind != allocatedPass.Kind || recorded.Wave != allocatedPass.Wave {
			return fmt.Errorf("persisted ledger pass %q does not match allocated pass kind/wave", recorded.PassID)
		}
		for _, outcome := range recorded.Outcomes {
			if _, ok := knownScenarios[outcomeKey(outcome.Axis, outcome.Scenario)]; !ok {
				return fmt.Errorf("persisted ledger outcome references unknown campaign scenario %q / %q", outcome.Axis, outcome.Scenario)
			}
		}
	}
	return nil
}

func validateCampaignAndLedger(state *runState, campaign *Campaign, value *ledger) error {
	if campaign == nil {
		if state.CampaignRequired || len(value.Passes) > 0 {
			return fmt.Errorf("%w: campaign is required for persisted run data", ErrCampaignInvalid)
		}
		return nil
	}
	return validatePersistedData(state, campaign, value)
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeBytes(path, data)
}

func writeBytes(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := rejectSymlink(path); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(paths.FileMode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return nil
}

func readStaging(path string, target any) error {
	data, err := readNoFollow(path, MaxStagingFileBytes)
	if err != nil {
		return fmt.Errorf("%w: read %s: %v", ErrInvalidStaging, path, err)
	}
	if err := decodeStrict(data, target); err != nil {
		return fmt.Errorf("%w: parse %s: %v", ErrInvalidStaging, path, err)
	}
	return nil
}

func queueEvent(state *runState, eventType events.Type, payload map[string]any) {
	payload["runId"] = state.RunID
	envelope := &outbox.Envelope{
		ULID:            atelierulid.New(),
		Type:            string(eventType),
		ClaudeSessionID: state.Session,
		Payload:         payload,
		CreatedAt:       time.Now().UTC(),
	}
	state.PendingEvents = append(state.PendingEvents, *envelope)
}

func flushPending(runID string) error {
	state, err := readRun(runID)
	if err != nil {
		return err
	}
	for len(state.PendingEvents) > 0 {
		if err := outbox.Write(&state.PendingEvents[0]); err != nil {
			return err
		}
		state.PendingEvents = state.PendingEvents[1:]
		if err := writeJSON(paths.ForgeRunState(runID), state); err != nil {
			return err
		}
	}
	return nil
}

func pruneContext(ctx context.Context, now time.Time) error {
	if err := ensureDir(paths.Forge()); err != nil {
		return err
	}
	entries, err := os.ReadDir(paths.Forge())
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || validateRunID(entry.Name()) != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if now.Sub(info.ModTime()) <= retention {
			continue
		}
		initialModTime := info.ModTime()
		if err := withRunLockContext(ctx, entry.Name(), func() error {
			children, err := os.ReadDir(paths.ForgeRun(entry.Name()))
			if err != nil {
				return err
			}
			lastModified := initialModTime
			for _, child := range children {
				if child.Name() == filepath.Base(paths.ForgeRunLock(entry.Name())) {
					continue
				}
				childInfo, err := child.Info()
				if err != nil {
					return err
				}
				if childInfo.ModTime().After(lastModified) {
					lastModified = childInfo.ModTime()
				}
			}
			if now.Sub(lastModified) <= retention {
				return nil
			}
			return os.RemoveAll(paths.ForgeRun(entry.Name()))
		}); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func cleanTicket(ticket string) (string, error) {
	ticket = strings.TrimSpace(ticket)
	if ticket == "" {
		return "", errors.New("ticket must be non-empty")
	}
	return ticket, nil
}
