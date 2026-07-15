package forge

import (
	"bytes"
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

	"github.com/valian-ca/homebrew-tools/internal/atelierd/events"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/outbox"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/paths"
	atelierulid "github.com/valian-ca/homebrew-tools/internal/atelierd/ulid"
)

const retention = 14 * 24 * time.Hour

func ensureDir(path string) error {
	if err := os.MkdirAll(path, paths.DirMode); err != nil {
		return err
	}
	return os.Chmod(path, paths.DirMode)
}

func validateRunID(runID string) error {
	parsed, err := oklogulid.ParseStrict(runID)
	if err != nil || parsed.String() != runID {
		return fmt.Errorf("%w: %s", ErrUnknownRun, runID)
	}
	return nil
}

func withRunLock(runID string, fn func() error) error {
	if err := validateRunID(runID); err != nil {
		return err
	}
	info, err := os.Stat(paths.ForgeRun(runID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %s", ErrUnknownRun, runID)
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s", ErrUnknownRun, runID)
	}
	lock := flock.New(paths.ForgeRunLock(runID), flock.SetPermissions(paths.FileMode))
	if err := lock.Lock(); err != nil {
		return fmt.Errorf("acquire forge run lock: %w", err)
	}
	defer func() { _ = lock.Unlock() }()
	if err := os.Chmod(paths.ForgeRunLock(runID), paths.FileMode); err != nil {
		return err
	}
	return fn()
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
	data, err := os.ReadFile(paths.ForgeRunState(runID))
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
	return &state, nil
}

func readCampaign(runID string) (*Campaign, error) {
	data, err := os.ReadFile(paths.ForgeCampaign(runID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrCampaignInvalid
		}
		return nil, err
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
	data, err := os.ReadFile(paths.ForgeLedger(runID))
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
	return os.Chmod(path, paths.FileMode)
}

func readStaging(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%w: read %s: %v", ErrInvalidStaging, path, err)
	}
	if err := decodeStrict(data, target); err != nil {
		return fmt.Errorf("%w: parse %s: %v", ErrInvalidStaging, path, err)
	}
	return nil
}

func emit(state *runState, eventType events.Type, payload map[string]any) error {
	payload["runId"] = state.RunID
	envelope := &outbox.Envelope{
		ULID:            atelierulid.New(),
		Type:            string(eventType),
		ClaudeSessionID: state.Session,
		Payload:         payload,
		CreatedAt:       time.Now().UTC(),
	}
	return outbox.Write(envelope)
}

func prune(now time.Time) error {
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
		if err := withRunLock(entry.Name(), func() error {
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
