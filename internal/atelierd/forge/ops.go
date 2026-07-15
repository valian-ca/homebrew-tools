package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/events"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/paths"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/ulid"
)

func Start(ticket, session string, cap int) (string, error) {
	return StartContext(context.Background(), ticket, session, cap)
}

func StartContext(ctx context.Context, ticket, session string, cap int) (string, error) {
	var err error
	ticket, err = cleanTicket(ticket)
	if err != nil {
		return "", err
	}
	if len(ticket) > MaxTextBytes {
		return "", fmt.Errorf("ticket exceeds %d bytes", MaxTextBytes)
	}
	if session == "" {
		return "", errors.New("session must be non-empty")
	}
	if len(session) > MaxIDBytes {
		return "", fmt.Errorf("session exceeds %d bytes", MaxIDBytes)
	}
	if cap <= 0 {
		return "", errors.New("cap must be greater than zero")
	}
	now := time.Now().UTC()
	if err := pruneContext(ctx, now); err != nil {
		if errors.Is(err, ErrRunBusy) {
			return "", ErrRunBusy
		}
		return "", fmt.Errorf("prune forge runs: %w", err)
	}
	runID := ulid.New()
	if err := createDir(paths.ForgeRun(runID)); err != nil {
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
		NextReview:    1,
		NextRepair:    1,
		CreatedAt:     now,
	}
	if err := withRunLockContext(ctx, runID, func() error {
		queueEvent(state, events.ForgeRunStart, map[string]any{"ticket": ticket, "cap": cap})
		return writeJSON(paths.ForgeRunState(runID), state)
	}); err != nil {
		_ = os.RemoveAll(paths.ForgeRun(runID))
		return "", err
	}
	return runID, nil
}

func Status(runID string) (RunStatus, error) {
	return StatusContext(context.Background(), runID)
}

func StatusContext(ctx context.Context, runID string) (RunStatus, error) {
	var result RunStatus
	err := withRunLockContext(ctx, runID, func() error {
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
	return StatusJSONContext(context.Background(), runID)
}

func StatusJSONContext(ctx context.Context, runID string) ([]byte, error) {
	status, err := StatusContext(ctx, runID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(status)
}

func OpenWave(runID string) (int, error) {
	return OpenWaveContext(context.Background(), runID)
}

func OpenWaveContext(ctx context.Context, runID string) (int, error) {
	var state *runState
	err := withRunLockContext(ctx, runID, func() error {
		var err error
		state, err = readRun(runID)
		if err != nil {
			return err
		}
		if state.WaveOpen {
			return fmt.Errorf("%w: wave %d is already open", ErrInvalidPass, state.Wave)
		}
		if len(state.Waves) > 0 {
			last := state.Waves[len(state.Waves)-1]
			if !last.Open && last.Findings != nil && *last.Findings == 0 {
				return fmt.Errorf("%w: most recent wave was dry", ErrInvalidPass)
			}
		}
		if state.Wave >= state.Cap {
			return fmt.Errorf("%w: cap %d", ErrWaveCap, state.Cap)
		}
		state.Wave++
		state.WaveOpen = true
		state.Waves = append(state.Waves, wave{Number: state.Wave, Open: true})
		queueEvent(state, events.ForgeWaveOpen, map[string]any{"wave": state.Wave})
		return writeJSON(paths.ForgeRunState(runID), state)
	})
	if err != nil {
		return 0, err
	}
	return state.Wave, nil
}

func CloseWave(runID string, findings int) (string, error) {
	return CloseWaveContext(context.Background(), runID, findings)
}

func CloseWaveContext(ctx context.Context, runID string, findings int) (string, error) {
	if findings < 0 {
		return "", fmt.Errorf("%w: findings must be non-negative", ErrInvalidPass)
	}
	var state *runState
	decision := "continue"
	err := withRunLockContext(ctx, runID, func() error {
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
		campaign, err := readCampaign(runID)
		if err != nil {
			return err
		}
		ledger, err := readLedger(runID)
		if err != nil {
			return err
		}
		if err := validatePersistedData(state, campaign, ledger); err != nil {
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
		queueEvent(state, events.ForgeWaveClose, map[string]any{
			"wave": state.Wave, "findings": findings, "decision": decision,
		})
		return writeJSON(paths.ForgeRunState(runID), state)
	})
	if err != nil {
		return "", err
	}
	return decision, nil
}

func NextPass(runID, kindValue string) (string, error) {
	return NextPassContext(context.Background(), runID, kindValue)
}

func NextPassContext(ctx context.Context, runID, kindValue string) (string, error) {
	kind, err := parsePassKind(kindValue)
	if err != nil {
		return "", err
	}
	var state *runState
	var created pass
	err = withRunLockContext(ctx, runID, func() error {
		var err error
		state, err = readRun(runID)
		if err != nil {
			return err
		}
		// A pass is the point at which the campaign becomes immutable. Validate
		// it before checking or changing pass state, and before creating any
		// capture directories, so a failed campaign save can still be retried.
		if _, err := readCampaign(runID); err != nil {
			return err
		}
		if state.OpenPass != "" {
			return fmt.Errorf("%w: pass %s is still open", ErrInvalidPass, state.OpenPass)
		}
		var sequence int
		switch kind {
		case passWave:
			if !state.WaveOpen {
				return fmt.Errorf("%w: wave pass requires an open wave", ErrInvalidPass)
			}
			sequence = state.Wave
			for _, existing := range state.Passes {
				if existing.Kind == passWave && existing.Wave == state.Wave {
					return fmt.Errorf("%w: wave %d already has a pass", ErrInvalidPass, state.Wave)
				}
			}
		case passReview:
			sequence = state.NextReview
		case passRepair:
			sequence = state.NextRepair
		}
		if kind != passWave {
			if sequence > MaxAuxiliaryPasses {
				return fmt.Errorf("%w: %s pass cap %d reached", ErrInvalidPass, kind, MaxAuxiliaryPasses)
			}
		}
		if len(state.Passes) >= MaxPasses {
			return fmt.Errorf("%w: total pass cap %d reached", ErrInvalidPass, MaxPasses)
		}
		created = pass{ID: fmt.Sprintf("%s-%d", kind, sequence), Kind: kind}
		if kind == passWave {
			created.Wave = state.Wave
		}
		captureDir := paths.ForgePassCaptures(runID, created.ID)
		if err := ensureDir(paths.ForgeCaptures(runID)); err != nil {
			return err
		}
		if err := createDir(captureDir); err != nil {
			return err
		}
		state.Passes = append(state.Passes, created)
		switch kind {
		case passReview:
			state.NextReview = sequence + 1
		case passRepair:
			state.NextRepair = sequence + 1
		}
		state.OpenPass = created.ID
		queueEvent(state, events.ForgePass, map[string]any{
			"passId": created.ID, "kind": string(created.Kind), "wave": created.Wave,
		})
		if err := writeJSON(paths.ForgeRunState(runID), state); err != nil {
			_ = os.Remove(captureDir)
			return err
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	path, err := filepath.Abs(paths.ForgePassCaptures(runID, created.ID))
	if err != nil {
		return "", err
	}
	return path, nil
}

func SaveCampaign(runID, stagingPath string) error {
	return SaveCampaignContext(context.Background(), runID, stagingPath)
}

func SaveCampaignContext(ctx context.Context, runID, stagingPath string) error {
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
	err := withRunLockContext(ctx, runID, func() error {
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
		if err := writeJSON(paths.ForgeCampaign(runID), &campaign); err != nil {
			return err
		}
		axes := len(campaign.Axes)
		scenarios := 0
		for _, axis := range campaign.Axes {
			scenarios += len(axis.Scenarios)
		}
		queueEvent(state, events.ForgeCampaignSaved, map[string]any{"axes": axes, "scenarios": scenarios})
		return writeJSON(paths.ForgeRunState(runID), state)
	})
	if err != nil {
		return err
	}
	return nil
}

func LoadCampaign(runID string) ([]byte, error) {
	return LoadCampaignContext(context.Background(), runID)
}

func LoadCampaignContext(ctx context.Context, runID string) ([]byte, error) {
	var result []byte
	err := withRunLockContext(ctx, runID, func() error {
		if _, err := readRun(runID); err != nil {
			return err
		}
		if _, err := readCampaign(runID); err != nil {
			return err
		}
		var err error
		result, err = readNoFollow(paths.ForgeCampaign(runID), MaxCampaignFileBytes)
		return err
	})
	return result, err
}

func RecordOutcome(runID, passID, stagingPath string) error {
	return RecordOutcomeContext(context.Background(), runID, passID, stagingPath)
}

func RecordOutcomeContext(ctx context.Context, runID, passID, stagingPath string) error {
	var batch OutcomeBatch
	if err := readStaging(stagingPath, &batch); err != nil {
		return err
	}
	if batch.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: outcome schemaVersion must be %d", ErrInvalidStaging, SchemaVersion)
	}
	var state *runState
	err := withRunLockContext(ctx, runID, func() error {
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
		if state.OpenPass != passID {
			return fmt.Errorf("%w: pass %q is not open", ErrInvalidPass, passID)
		}
		ledger, err := readLedger(runID)
		if err != nil {
			return err
		}
		campaign, err := readCampaign(runID)
		if err != nil {
			return err
		}
		for _, existing := range ledger.Passes {
			if existing.PassID == passID {
				total, err := validateOutcomes(&batch, campaign)
				if err != nil {
					return fmt.Errorf("%w: stale pass outcome: %v", ErrInvalidPass, err)
				}
				if existing.Kind != target.Kind || existing.Wave != target.Wave ||
					!reflect.DeepEqual(existing.Outcomes, batch.Outcomes) || existing.Counts != total {
					return fmt.Errorf("%w: stale pass outcome does not match persisted pass %q", ErrInvalidPass, passID)
				}
				state.OpenPass = ""
				queueOutcomeRecorded(state, target, total)
				return writeJSON(paths.ForgeRunState(runID), state)
			}
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
		state.OpenPass = ""
		queueOutcomeRecorded(state, target, total)
		if err := writeJSON(paths.ForgeRunState(runID), state); err != nil {
			return err
		}
		return nil
	})
	return err
}

func queueOutcomeRecorded(state *runState, target *pass, total counts) {
	queueEvent(state, events.ForgeOutcomeRecorded, map[string]any{
		"passId": target.ID, "kind": string(target.Kind), "wave": target.Wave,
		"pass": total.Pass, "finding": total.Finding, "notExercised": total.NotExercised,
	})
}

func SetRef(runID, key, value string) error {
	return SetRefContext(context.Background(), runID, key, value)
}

func SetRefContext(ctx context.Context, runID, key, value string) error {
	if key != "report" && key != "testplan" {
		return fmt.Errorf("key must be report or testplan, got %q", key)
	}
	if value == "" {
		return errors.New("reference value must be non-empty")
	}
	if len(value) > MaxTextBytes {
		return fmt.Errorf("reference value exceeds %d bytes", MaxTextBytes)
	}
	var state *runState
	err := withRunLockContext(ctx, runID, func() error {
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
		switch key {
		case "report":
			queueEvent(state, events.ForgeReportLinked, map[string]any{"report": value})
		case "testplan":
			queueEvent(state, events.ForgeTestplanLinked, map[string]any{"testplan": value})
		}
		return writeJSON(paths.ForgeRunState(runID), state)
	})
	if err != nil {
		return err
	}
	return nil
}

func GetRef(runID, key string) (string, error) {
	return GetRefContext(context.Background(), runID, key)
}

func GetRefContext(ctx context.Context, runID, key string) (string, error) {
	if key != "report" && key != "testplan" {
		return "", fmt.Errorf("key must be report or testplan, got %q", key)
	}
	var value string
	err := withRunLockContext(ctx, runID, func() error {
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
