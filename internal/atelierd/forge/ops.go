package forge

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/events"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/paths"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/ulid"
)

func Start(ticket, session string, cap int) (string, error) {
	var err error
	ticket, err = cleanTicket(ticket)
	if err != nil {
		return "", err
	}
	if session == "" {
		return "", errors.New("session must be non-empty")
	}
	if cap <= 0 {
		return "", errors.New("cap must be greater than zero")
	}
	now := time.Now().UTC()
	if err := prune(now); err != nil {
		return "", fmt.Errorf("prune forge runs: %w", err)
	}
	runID := ulid.New()
	if err := os.Mkdir(paths.ForgeRun(runID), paths.DirMode); err != nil {
		return "", err
	}
	state := &runState{
		SchemaVersion: SchemaVersion,
		RunID:         runID,
		Ticket:        ticket,
		Session:       session,
		Cap:           cap,
		Waves:         []wave{},
		Passes:        []pass{},
		CreatedAt:     now,
	}
	if err := withRunLock(runID, func() error {
		return writeJSON(paths.ForgeRunState(runID), state)
	}); err != nil {
		_ = os.RemoveAll(paths.ForgeRun(runID))
		return "", err
	}
	if err := emit(state, events.ForgeRunStart, map[string]any{"ticket": ticket, "cap": cap}); err != nil {
		return "", err
	}
	return runID, nil
}

func Status(runID string) (RunStatus, error) {
	var result RunStatus
	err := withRunLock(runID, func() error {
		state, err := readRun(runID)
		if err != nil {
			return err
		}
		result.SchemaVersion = state.SchemaVersion
		result.RunID = state.RunID
		result.Ticket = state.Ticket
		result.Wave = state.Wave
		result.WaveOpen = state.WaveOpen
		result.OpenPass = state.OpenPass
		result.Refs.Report = state.Refs.Report
		result.Refs.Testplan = state.Refs.Testplan
		return nil
	})
	return result, err
}

func StatusJSON(runID string) ([]byte, error) {
	status, err := Status(runID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(status)
}

func OpenWave(runID string) (int, error) {
	var state *runState
	err := withRunLock(runID, func() error {
		var err error
		state, err = readRun(runID)
		if err != nil {
			return err
		}
		if state.WaveOpen {
			return fmt.Errorf("%w: wave %d is already open", ErrInvalidPass, state.Wave)
		}
		if state.Wave >= state.Cap {
			return fmt.Errorf("%w: cap %d", ErrWaveCap, state.Cap)
		}
		state.Wave++
		state.WaveOpen = true
		state.Waves = append(state.Waves, wave{Number: state.Wave, Open: true})
		return writeJSON(paths.ForgeRunState(runID), state)
	})
	if err != nil {
		return 0, err
	}
	if err := emit(state, events.ForgeWaveOpen, map[string]any{"wave": state.Wave}); err != nil {
		return 0, err
	}
	return state.Wave, nil
}

func CloseWave(runID string, findings int) (string, error) {
	if findings < 0 {
		return "", fmt.Errorf("%w: findings must be non-negative", ErrInvalidPass)
	}
	var state *runState
	decision := "continue"
	err := withRunLock(runID, func() error {
		var err error
		state, err = readRun(runID)
		if err != nil {
			return err
		}
		if !state.WaveOpen || len(state.Waves) == 0 {
			return fmt.Errorf("%w: no wave is open", ErrInvalidPass)
		}
		if state.OpenPass != "" {
			return fmt.Errorf("%w: pass %s is still open", ErrInvalidPass, state.OpenPass)
		}
		var currentPass *pass
		for i := range state.Passes {
			if state.Passes[i].Kind == passWave && state.Passes[i].Wave == state.Wave {
				currentPass = &state.Passes[i]
				break
			}
		}
		if currentPass == nil {
			return fmt.Errorf("%w: wave %d has no pass", ErrInvalidPass, state.Wave)
		}
		ledger, err := readLedger(runID)
		if err != nil {
			return err
		}
		var recorded *ledgerPass
		for i := range ledger.Passes {
			if ledger.Passes[i].PassID == currentPass.ID {
				recorded = &ledger.Passes[i]
				break
			}
		}
		if recorded == nil {
			return fmt.Errorf("%w: wave pass %s has no recorded outcome", ErrInvalidPass, currentPass.ID)
		}
		if recorded.Kind != passWave || recorded.Wave != state.Wave {
			return fmt.Errorf("%w: ledger entry %s does not belong to wave %d", ErrInvalidPass, recorded.PassID, state.Wave)
		}
		if findings != recorded.Counts.Finding {
			return fmt.Errorf("%w: findings %d do not match ledger count %d", ErrInvalidPass, findings, recorded.Counts.Finding)
		}
		if findings == 0 {
			decision = "dry"
		} else if state.Wave >= state.Cap {
			decision = "cap"
		}
		state.WaveOpen = false
		state.Waves[len(state.Waves)-1].Open = false
		state.Waves[len(state.Waves)-1].Findings = &findings
		return writeJSON(paths.ForgeRunState(runID), state)
	})
	if err != nil {
		return "", err
	}
	if err := emit(state, events.ForgeWaveClose, map[string]any{
		"wave": state.Wave, "findings": findings, "decision": decision,
	}); err != nil {
		return "", err
	}
	return decision, nil
}

func NextPass(runID, kindValue string) (string, error) {
	kind, err := parsePassKind(kindValue)
	if err != nil {
		return "", err
	}
	var state *runState
	var created pass
	err = withRunLock(runID, func() error {
		var err error
		state, err = readRun(runID)
		if err != nil {
			return err
		}
		if state.OpenPass != "" {
			return fmt.Errorf("%w: pass %s is still open", ErrInvalidPass, state.OpenPass)
		}
		sequence := 1
		if kind == passWave {
			if !state.WaveOpen {
				return fmt.Errorf("%w: wave pass requires an open wave", ErrInvalidPass)
			}
			sequence = state.Wave
			for _, existing := range state.Passes {
				if existing.Kind == passWave && existing.Wave == state.Wave {
					return fmt.Errorf("%w: wave %d already has a pass", ErrInvalidPass, state.Wave)
				}
			}
		} else {
			for _, existing := range state.Passes {
				if existing.Kind == kind {
					sequence++
				}
			}
		}
		created = pass{ID: fmt.Sprintf("%s-%d", kind, sequence), Kind: kind}
		if kind == passWave {
			created.Wave = state.Wave
		}
		captureDir := paths.ForgePassCaptures(runID, created.ID)
		if err := ensureDir(paths.ForgeCaptures(runID)); err != nil {
			return err
		}
		if err := os.Mkdir(captureDir, paths.DirMode); err != nil {
			return err
		}
		state.Passes = append(state.Passes, created)
		state.OpenPass = created.ID
		if err := writeJSON(paths.ForgeRunState(runID), state); err != nil {
			_ = os.Remove(captureDir)
			return err
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if err := emit(state, events.ForgePass, map[string]any{
		"passId": created.ID, "kind": string(created.Kind), "wave": created.Wave,
	}); err != nil {
		return "", err
	}
	path, err := filepath.Abs(paths.ForgePassCaptures(runID, created.ID))
	if err != nil {
		return "", err
	}
	return path, nil
}

func SaveCampaign(runID, stagingPath string) error {
	var campaign Campaign
	if err := readStaging(stagingPath, &campaign); err != nil {
		return err
	}
	if campaign.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: campaign schemaVersion must be %d", ErrInvalidStaging, SchemaVersion)
	}
	if err := validateCampaign(&campaign); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidStaging, err)
	}
	var state *runState
	err := withRunLock(runID, func() error {
		var err error
		state, err = readRun(runID)
		if err != nil {
			return err
		}
		ledger, err := readLedger(runID)
		if err != nil {
			return err
		}
		if len(state.Passes) > 0 || len(ledger.Passes) > 0 {
			return fmt.Errorf("%w: campaign is frozen after pass allocation", ErrInvalidStaging)
		}
		return writeJSON(paths.ForgeCampaign(runID), &campaign)
	})
	if err != nil {
		return err
	}
	axes := len(campaign.Axes)
	scenarios := 0
	for _, axis := range campaign.Axes {
		scenarios += len(axis.Scenarios)
	}
	return emit(state, events.ForgeCampaignSaved, map[string]any{"axes": axes, "scenarios": scenarios})
}

func LoadCampaign(runID string) ([]byte, error) {
	var result []byte
	err := withRunLock(runID, func() error {
		if _, err := readRun(runID); err != nil {
			return err
		}
		if _, err := readCampaign(runID); err != nil {
			return err
		}
		var err error
		result, err = os.ReadFile(paths.ForgeCampaign(runID))
		return err
	})
	return result, err
}

func RecordOutcome(runID, passID, stagingPath string) error {
	var batch OutcomeBatch
	if err := readStaging(stagingPath, &batch); err != nil {
		return err
	}
	if batch.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: outcome schemaVersion must be %d", ErrInvalidStaging, SchemaVersion)
	}
	var state *runState
	err := withRunLock(runID, func() error {
		var err error
		state, err = readRun(runID)
		if err != nil {
			return err
		}
		var target *pass
		for i := range state.Passes {
			if state.Passes[i].ID == passID {
				target = &state.Passes[i]
				break
			}
		}
		if target == nil {
			return fmt.Errorf("%w: unknown pass %q", ErrInvalidPass, passID)
		}
		ledger, err := readLedger(runID)
		if err != nil {
			return err
		}
		for _, existing := range ledger.Passes {
			if existing.PassID == passID {
				if state.OpenPass == passID {
					state.OpenPass = ""
					return writeJSON(paths.ForgeRunState(runID), state)
				}
				return fmt.Errorf("%w: pass %q already recorded", ErrInvalidPass, passID)
			}
		}
		campaign, err := readCampaign(runID)
		if err != nil {
			return err
		}
		total, err := validateOutcomes(&batch, campaign)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidStaging, err)
		}
		ledger.Passes = append(ledger.Passes, ledgerPass{
			PassID: passID, Kind: target.Kind, Wave: target.Wave, Outcomes: batch.Outcomes, Counts: total,
		})
		if err := writeJSON(paths.ForgeLedger(runID), ledger); err != nil {
			return err
		}
		if state.OpenPass == passID {
			state.OpenPass = ""
			if err := writeJSON(paths.ForgeRunState(runID), state); err != nil {
				return err
			}
		}
		return nil
	})
	return err
}

func SetRef(runID, key, value string) error {
	if key != "report" && key != "testplan" {
		return fmt.Errorf("key must be report or testplan, got %q", key)
	}
	if value == "" {
		return errors.New("reference value must be non-empty")
	}
	var state *runState
	err := withRunLock(runID, func() error {
		var err error
		state, err = readRun(runID)
		if err != nil {
			return err
		}
		if key == "report" {
			state.Refs.Report = value
		} else {
			state.Refs.Testplan = value
		}
		return writeJSON(paths.ForgeRunState(runID), state)
	})
	if err != nil {
		return err
	}
	if key == "report" {
		return emit(state, events.ForgeReportLinked, map[string]any{"report": value})
	}
	return nil
}

func GetRef(runID, key string) (string, error) {
	if key != "report" && key != "testplan" {
		return "", fmt.Errorf("key must be report or testplan, got %q", key)
	}
	var value string
	err := withRunLock(runID, func() error {
		state, err := readRun(runID)
		if err != nil {
			return err
		}
		if key == "report" {
			value = state.Refs.Report
		} else {
			value = state.Refs.Testplan
		}
		return nil
	})
	return value, err
}
