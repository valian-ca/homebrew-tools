package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
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
	var runID string
	err = withForgeLockContext(ctx, func() error {
		now := time.Now().UTC()
		if err := pruneContext(ctx, now); err != nil {
			if errors.Is(err, ErrRunBusy) {
				return ErrRunBusy
			}
			return fmt.Errorf("prune forge runs: %w", err)
		}
		runID = ulid.New()
		if err := createDir(paths.ForgeRun(runID)); err != nil {
			return err
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
			return err
		}
		return nil
	})
	return runID, err
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

func FindRun(ticket, session string) (string, error) {
	return FindRunContext(context.Background(), ticket, session)
}

func FindRunContext(ctx context.Context, ticket, session string) (string, error) {
	var err error
	if ticket != "" {
		ticket, err = cleanTicket(ticket)
		if err != nil {
			return "", err
		}
		if len(ticket) > MaxTextBytes {
			return "", fmt.Errorf("ticket exceeds %d bytes", MaxTextBytes)
		}
	}
	if len(session) > MaxIDBytes {
		return "", fmt.Errorf("session exceeds %d bytes", MaxIDBytes)
	}
	if ticket == "" && session == "" {
		return "", errors.New("ticket or session must be provided")
	}

	matches := make([]string, 0, 1)
	err = withForgeLockContext(ctx, func() error {
		entries, err := os.ReadDir(paths.Forge())
		if err != nil {
			return err
		}
		for _, entry := range entries {
			runID := entry.Name()
			if !entry.IsDir() || validateRunID(runID) != nil {
				continue
			}
			state, err := readRun(runID)
			if errors.Is(err, ErrUnknownRun) {
				continue
			}
			if err != nil {
				return err
			}
			if predatesContract3(runID) {
				continue
			}
			if (ticket == "" || state.Ticket == ticket) && (session == "" || state.Session == session) {
				matches = append(matches, runID)
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", ErrUnknownRun
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("%w: %s", ErrAmbiguousRun, strings.Join(matches, ", "))
	}
	return matches[0], nil
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

func CloseWave(runID string, fixed int) (string, error) {
	return CloseWaveContext(context.Background(), runID, fixed)
}

func CloseWaveContext(ctx context.Context, runID string, fixed int) (string, error) {
	if fixed < 0 {
		return "", fmt.Errorf("%w: fixed must be non-negative", ErrInvalidPass)
	}
	var state *runState
	decision := "continue"
	err := withRunLockContext(ctx, runID, func() error {
		var err error
		state, err = readRun(runID)
		if err != nil {
			return err
		}
		campaign, err := readCampaignIfPresent(runID)
		if err != nil {
			return err
		}
		ledger, err := readLedger(runID)
		if err != nil {
			return err
		}
		if err := validateCampaignAndLedger(state, campaign, ledger); err != nil {
			return err
		}
		if campaign != nil && !state.CampaignRequired {
			state.CampaignRequired = true
			if err := writeJSON(paths.ForgeRunState(runID), state); err != nil {
				return err
			}
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
		findings := 0
		if campaign != nil {
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
			findings = recorded.Counts.Finding
		}
		if fixed == 0 {
			decision = "dry"
		} else if state.Wave >= state.Cap {
			decision = "cap"
		}
		state.WaveOpen = false
		state.Waves[len(state.Waves)-1].Open = false
		state.Waves[len(state.Waves)-1].Findings = &findings
		state.Waves[len(state.Waves)-1].Fixed = &fixed
		queueEvent(state, events.ForgeWaveClose, map[string]any{
			"wave": state.Wave, "findings": findings, "fixed": fixed, "decision": decision,
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
		campaign, err := readCampaignIfPresent(runID)
		if err != nil {
			return err
		}
		ledger, err := readLedger(runID)
		if err != nil {
			return err
		}
		if err := validateCampaignAndLedger(state, campaign, ledger); err != nil {
			return err
		}
		if campaign != nil && !state.CampaignRequired {
			state.CampaignRequired = true
			if err := writeJSON(paths.ForgeRunState(runID), state); err != nil {
				return err
			}
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
		if campaign != nil {
			state.OpenPass = created.ID
		}
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

func ShowPass(runID, passID, kindValue string, waveNumber int) (PassStatus, error) {
	return ShowPassContext(context.Background(), runID, passID, kindValue, waveNumber)
}

func ShowPassContext(ctx context.Context, runID, passID, kindValue string, waveNumber int) (PassStatus, error) {
	var result PassStatus
	if passID == "" && kindValue == "" {
		return result, fmt.Errorf("%w: pass or kind must be provided", ErrInvalidPass)
	}
	if passID != "" && (kindValue != "" || waveNumber != 0) {
		return result, fmt.Errorf("%w: pass cannot be combined with kind or wave", ErrInvalidPass)
	}
	if waveNumber < 0 {
		return result, fmt.Errorf("%w: wave must be non-negative", ErrInvalidPass)
	}
	var kind passKind
	var err error
	if kindValue != "" {
		kind, err = parsePassKind(kindValue)
		if err != nil {
			return result, err
		}
		if waveNumber > 0 && kind != passWave {
			return result, fmt.Errorf("%w: wave selector requires kind wave", ErrInvalidPass)
		}
	}

	err = withRunLockContext(ctx, runID, func() error {
		state, err := readRun(runID)
		if err != nil {
			return err
		}
		campaign, err := readCampaignIfPresent(runID)
		if err != nil {
			return err
		}
		ledger, err := readLedger(runID)
		if err != nil {
			return err
		}
		if err := validateCampaignAndLedger(state, campaign, ledger); err != nil {
			return err
		}
		strict := state.CampaignRequired || campaign != nil
		selectedWave := waveNumber
		if kind == passWave && selectedWave == 0 {
			selectedWave = state.Wave
		}
		var selected *pass
		for i := len(state.Passes) - 1; i >= 0; i-- {
			candidate := &state.Passes[i]
			if passID != "" {
				if candidate.ID == passID {
					selected = candidate
					break
				}
				continue
			}
			if candidate.Kind == kind && (kind != passWave || candidate.Wave == selectedWave) {
				selected = candidate
				break
			}
		}
		if selected == nil {
			return fmt.Errorf("%w: no allocated pass matches", ErrInvalidPass)
		}
		if err := validateAllocatedPass(*selected); err != nil {
			return err
		}
		captureDir, err := filepath.Abs(paths.ForgePassCaptures(runID, selected.ID))
		if err != nil {
			return err
		}
		if err := validateExistingDir(captureDir); err != nil {
			return fmt.Errorf("read capture directory: %w", err)
		}
		complete := !strict
		if strict {
			for _, recorded := range ledger.Passes {
				if recorded.PassID == selected.ID {
					complete = true
					break
				}
			}
		}
		result = PassStatus{
			RunID:            runID,
			PassID:           selected.ID,
			Kind:             string(selected.Kind),
			Wave:             selected.Wave,
			CaptureDir:       captureDir,
			CampaignRequired: strict,
			Complete:         complete,
		}
		return nil
	})
	return result, err
}

func ShowPassJSONContext(ctx context.Context, runID, passID, kindValue string, waveNumber int) ([]byte, error) {
	status, err := ShowPassContext(ctx, runID, passID, kindValue, waveNumber)
	if err != nil {
		return nil, err
	}
	return json.Marshal(status)
}

func validateAllocatedPass(value pass) error {
	kind := value.Kind
	switch kind {
	case passWave, passReview, passRepair:
	default:
		return fmt.Errorf("invalid persisted pass kind %q", value.Kind)
	}
	prefix := string(kind) + "-"
	if !strings.HasPrefix(value.ID, prefix) || filepath.Base(value.ID) != value.ID {
		return fmt.Errorf("invalid persisted pass id %q", value.ID)
	}
	sequence, err := strconv.Atoi(strings.TrimPrefix(value.ID, prefix))
	if err != nil || sequence <= 0 {
		return fmt.Errorf("invalid persisted pass id %q", value.ID)
	}
	if kind == passWave && value.Wave != sequence {
		return fmt.Errorf("invalid persisted wave pass %q", value.ID)
	}
	if kind != passWave && value.Wave != 0 {
		return fmt.Errorf("invalid persisted auxiliary pass %q", value.ID)
	}
	return nil
}

func SaveCampaign(runID, stagingPath string) error {
	return SaveCampaignContext(context.Background(), runID, stagingPath)
}

func SaveCampaignContext(ctx context.Context, runID, stagingPath string) error {
	campaign, err := readCampaignStaging(stagingPath)
	if err != nil {
		return err
	}
	return withRunLockContext(ctx, runID, func() error {
		state, err := readRun(runID)
		if err != nil {
			return err
		}
		ledger, err := readLedger(runID)
		if err != nil {
			return err
		}
		if len(ledger.Passes) > 0 {
			return fmt.Errorf("%w: campaign is frozen after the first recorded outcome", ErrInvalidStaging)
		}
		if len(state.Passes) > 0 && !state.CampaignRequired {
			return fmt.Errorf("%w: run allocated passes without a campaign", ErrInvalidStaging)
		}
		if err := writeCampaign(runID, campaign); err != nil {
			return err
		}
		state.CampaignRequired = true
		queueCampaignSaved(state, campaign)
		return writeJSON(paths.ForgeRunState(runID), state)
	})
}

func readCampaignStaging(stagingPath string) (*Campaign, error) {
	var campaign Campaign
	if err := readStaging(stagingPath, &campaign); err != nil {
		return nil, err
	}
	if campaign.SchemaVersion != CampaignSchemaVersion {
		return nil, fmt.Errorf("%w: campaign schemaVersion must be %d", ErrInvalidStaging, CampaignSchemaVersion)
	}
	for _, axis := range campaign.Axes {
		for _, scenario := range axis.Scenarios {
			if scenario.Wave != 0 {
				return nil, fmt.Errorf("%w: scenario %q must not carry wave; atelierd sets it on append", ErrInvalidStaging, scenario.Title)
			}
		}
	}
	if err := validateCampaign(&campaign); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidStaging, err)
	}
	return &campaign, nil
}

func queueCampaignSaved(state *runState, campaign *Campaign) {
	scenarios := 0
	for _, axis := range campaign.Axes {
		scenarios += len(axis.Scenarios)
	}
	queueEvent(state, events.ForgeCampaignSaved, map[string]any{"axes": len(campaign.Axes), "scenarios": scenarios})
}

func AppendCampaign(runID string, waveNumber int, stagingPath string) error {
	return AppendCampaignContext(context.Background(), runID, waveNumber, stagingPath)
}

func AppendCampaignContext(ctx context.Context, runID string, waveNumber int, stagingPath string) error {
	if waveNumber < 2 {
		return fmt.Errorf("%w: campaign append requires wave 2 or later, got %d", ErrInvalidStaging, waveNumber)
	}
	additions, err := readCampaignStaging(stagingPath)
	if err != nil {
		return err
	}
	return withRunLockContext(ctx, runID, func() error {
		state, err := readRun(runID)
		if err != nil {
			return err
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
		if !state.WaveOpen || state.Wave != waveNumber {
			return fmt.Errorf("%w: wave %d is not the open wave", ErrInvalidStaging, waveNumber)
		}
		for _, recorded := range ledger.Passes {
			if recorded.Kind == passWave && recorded.Wave == waveNumber {
				return fmt.Errorf("%w: wave %d already has a recorded outcome", ErrInvalidStaging, waveNumber)
			}
		}
		if err := checkAppendBudget(campaign, ledger, additions, waveNumber); err != nil {
			return err
		}
		for _, addition := range additions.Axes {
			index := -1
			for i := range campaign.Axes {
				if campaign.Axes[i].Title == addition.Title {
					index = i
					break
				}
			}
			if index == -1 {
				campaign.Axes = append(campaign.Axes, Axis{Title: addition.Title})
				index = len(campaign.Axes) - 1
			}
			for _, scenario := range addition.Scenarios {
				for _, existing := range campaign.Axes[index].Scenarios {
					if existing.Title == scenario.Title {
						return fmt.Errorf("%w: axis %q already has scenario %q", ErrInvalidStaging, addition.Title, scenario.Title)
					}
				}
				scenario.Wave = waveNumber
				campaign.Axes[index].Scenarios = append(campaign.Axes[index].Scenarios, scenario)
			}
		}
		if err := validateCampaign(campaign); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidStaging, err)
		}
		if err := writeCampaign(runID, campaign); err != nil {
			return err
		}
		queueCampaignSaved(state, campaign)
		return writeJSON(paths.ForgeRunState(runID), state)
	})
}

func checkAppendBudget(campaign *Campaign, value *ledger, additions *Campaign, waveNumber int) error {
	appended := make(map[string]int)
	for _, axis := range campaign.Axes {
		for _, scenario := range axis.Scenarios {
			if scenario.Wave == waveNumber {
				appended[axis.Title]++
			}
		}
	}
	for _, axis := range additions.Axes {
		appended[axis.Title] += len(axis.Scenarios)
	}
	if waveNumber > 2 {
		total := 0
		for _, count := range appended {
			total += count
		}
		if total > SharedAppendBudget {
			return fmt.Errorf("%w: wave %d accepts at most %d appended scenarios, got %d", ErrInvalidStaging, waveNumber, SharedAppendBudget, total)
		}
		return nil
	}
	bitten := make(map[string]bool)
	for _, recorded := range value.Passes {
		if recorded.Kind != passWave || recorded.Wave != 1 {
			continue
		}
		for _, outcome := range recorded.Outcomes {
			if outcome.Status == "finding" {
				bitten[outcome.Axis] = true
			}
		}
	}
	shared := 0
	for title, count := range appended {
		if !bitten[title] {
			shared += count
			continue
		}
		if count > BittenAxisAppendBudget {
			return fmt.Errorf("%w: bitten axis %q accepts at most %d appended scenarios in wave 2, got %d", ErrInvalidStaging, title, BittenAxisAppendBudget, count)
		}
	}
	if shared > SharedAppendBudget {
		return fmt.Errorf("%w: axes without a wave-1 finding share at most %d appended scenarios in wave 2, got %d", ErrInvalidStaging, SharedAppendBudget, shared)
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
	if batch.SchemaVersion != CampaignSchemaVersion {
		return fmt.Errorf("%w: outcome schemaVersion must be %d", ErrInvalidStaging, CampaignSchemaVersion)
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
		campaign, err := readCampaign(runID)
		if err != nil {
			return err
		}
		ledger, err := readLedger(runID)
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

// RunSession reads the session a run was started under without the run
// lock: it only feeds a display-only warning.
func RunSession(runID string) (string, error) {
	if err := validateRunID(runID); err != nil {
		return "", err
	}
	state, err := readRun(runID)
	if err != nil {
		return "", err
	}
	return state.Session, nil
}
