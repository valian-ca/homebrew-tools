package forge

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/outbox"
)

const (
	SchemaVersion   = 1
	ContractVersion = 1
	DefaultCap      = 4
	// Normal waves remain controlled by the run's --cap value. Each auxiliary
	// stream has its own v1 bound so review/repair cannot grow without limit.
	MaxAuxiliaryPasses = 32

	// V1 limits are deliberately conservative: forge state is local, small,
	// and is written synchronously. They also bound the amount of JSON a
	// command will accept before decoding it.
	MaxRunFileBytes      = 256 * 1024
	MaxCampaignFileBytes = 256 * 1024
	MaxLedgerFileBytes   = 512 * 1024
	MaxStagingFileBytes  = 256 * 1024
	MaxAxes              = 32
	MaxScenariosPerAxis  = 64
	MaxScenarios         = 512
	MaxSteps             = 32
	MaxOutcomes          = 512
	MaxTextBytes         = 4096
	MaxIDBytes           = 128
	MaxWaves             = 64
	// The persisted total is the sum of the independently bounded streams.
	MaxPasses        = MaxWaves + 2*MaxAuxiliaryPasses
	MaxPendingEvents = 256
)

var (
	ErrUnknownRun      = errors.New("forge: unknown run")
	ErrInvalidPass     = errors.New("forge: invalid pass request")
	ErrCampaignInvalid = errors.New("forge: campaign absent or invalid")
	ErrWaveCap         = errors.New("forge: wave cap reached")
	ErrInvalidStaging  = errors.New("forge: invalid staging file")
	ErrRunBusy         = errors.New("run busy")
)

type passKind string

const (
	passWave   passKind = "wave"
	passReview passKind = "review"
	passRepair passKind = "repair"
)

type refs struct {
	Report   string `json:"report"`
	Testplan string `json:"testplan"`
}

type wave struct {
	Number   int  `json:"number"`
	Open     bool `json:"open"`
	Findings *int `json:"findings,omitempty"`
}

type pass struct {
	ID   string   `json:"id"`
	Kind passKind `json:"kind"`
	Wave int      `json:"wave,omitempty"`
}

type runState struct {
	SchemaVersion int               `json:"schemaVersion"`
	RunID         string            `json:"runId"`
	Ticket        string            `json:"ticket"`
	Session       string            `json:"session"`
	Cap           int               `json:"cap"`
	Wave          int               `json:"wave"`
	WaveOpen      bool              `json:"waveOpen"`
	OpenPass      string            `json:"openPass"`
	Waves         []wave            `json:"waves"`
	Passes        []pass            `json:"passes"`
	NextReview    int               `json:"nextReview"`
	NextRepair    int               `json:"nextRepair"`
	Refs          refs              `json:"refs"`
	CreatedAt     time.Time         `json:"createdAt"`
	PendingEvents []outbox.Envelope `json:"pendingEvents,omitempty"`
}

type RunStatus struct {
	SchemaVersion int    `json:"schemaVersion"`
	RunID         string `json:"runId"`
	Ticket        string `json:"ticket"`
	Wave          int    `json:"wave"`
	WaveOpen      bool   `json:"waveOpen"`
	OpenPass      string `json:"openPass"`
	Refs          struct {
		Report   string `json:"report"`
		Testplan string `json:"testplan"`
	} `json:"refs"`
}

type Scenario struct {
	Title    string   `json:"title"`
	Steps    []string `json:"steps"`
	Expected string   `json:"expected"`
}

type Axis struct {
	Title     string     `json:"title"`
	Scenarios []Scenario `json:"scenarios"`
}

type Campaign struct {
	SchemaVersion int    `json:"schemaVersion"`
	Axes          []Axis `json:"axes"`
}

type Outcome struct {
	Axis     string `json:"axis"`
	Scenario string `json:"scenario"`
	Status   string `json:"status"`
	Reason   string `json:"reason,omitempty"`
}

type OutcomeBatch struct {
	SchemaVersion int       `json:"schemaVersion"`
	Outcomes      []Outcome `json:"outcomes"`
}

type counts struct {
	Pass         int `json:"pass"`
	Finding      int `json:"finding"`
	NotExercised int `json:"not_exercised"`
}

type ledgerPass struct {
	PassID   string    `json:"passId"`
	Kind     passKind  `json:"kind"`
	Wave     int       `json:"wave,omitempty"`
	Outcomes []Outcome `json:"outcomes"`
	Counts   counts    `json:"counts"`
}

type ledger struct {
	SchemaVersion int          `json:"schemaVersion"`
	Passes        []ledgerPass `json:"passes"`
}

func parsePassKind(value string) (passKind, error) {
	kind := passKind(value)
	switch kind {
	case passWave, passReview, passRepair:
		return kind, nil
	default:
		return "", fmt.Errorf("%w: unknown kind %q", ErrInvalidPass, value)
	}
}

func validateCampaign(c *Campaign) error {
	if len(c.Axes) == 0 {
		return errors.New("campaign must contain at least one axis")
	}
	if len(c.Axes) > MaxAxes {
		return fmt.Errorf("campaign has %d axes; maximum is %d", len(c.Axes), MaxAxes)
	}
	axes := make(map[string]struct{}, len(c.Axes))
	totalScenarios := 0
	for _, axis := range c.Axes {
		if len(axis.Title) > MaxTextBytes {
			return fmt.Errorf("axis title exceeds %d bytes", MaxTextBytes)
		}
		if strings.TrimSpace(axis.Title) == "" {
			return errors.New("axis title must be non-empty")
		}
		if _, exists := axes[axis.Title]; exists {
			return fmt.Errorf("duplicate axis %q", axis.Title)
		}
		axes[axis.Title] = struct{}{}
		if len(axis.Scenarios) == 0 {
			return fmt.Errorf("axis %q must contain at least one scenario", axis.Title)
		}
		if len(axis.Scenarios) > MaxScenariosPerAxis {
			return fmt.Errorf("axis %q has %d scenarios; maximum is %d", axis.Title, len(axis.Scenarios), MaxScenariosPerAxis)
		}
		totalScenarios += len(axis.Scenarios)
		if totalScenarios > MaxScenarios {
			return fmt.Errorf("campaign has more than %d scenarios", MaxScenarios)
		}
		scenarios := make(map[string]struct{}, len(axis.Scenarios))
		for _, scenario := range axis.Scenarios {
			if len(scenario.Title) > MaxTextBytes || len(scenario.Expected) > MaxTextBytes {
				return fmt.Errorf("scenario title and expected result must not exceed %d bytes", MaxTextBytes)
			}
			if strings.TrimSpace(scenario.Title) == "" {
				return fmt.Errorf("axis %q has an empty scenario title", axis.Title)
			}
			if _, exists := scenarios[scenario.Title]; exists {
				return fmt.Errorf("axis %q has duplicate scenario %q", axis.Title, scenario.Title)
			}
			scenarios[scenario.Title] = struct{}{}
			if len(scenario.Steps) == 0 {
				return fmt.Errorf("scenario %q must contain at least one step", scenario.Title)
			}
			if len(scenario.Steps) > MaxSteps {
				return fmt.Errorf("scenario %q has %d steps; maximum is %d", scenario.Title, len(scenario.Steps), MaxSteps)
			}
			for _, step := range scenario.Steps {
				if len(step) > MaxTextBytes {
					return fmt.Errorf("scenario %q step exceeds %d bytes", scenario.Title, MaxTextBytes)
				}
				if strings.TrimSpace(step) == "" {
					return fmt.Errorf("scenario %q has an empty step", scenario.Title)
				}
			}
			if strings.TrimSpace(scenario.Expected) == "" {
				return fmt.Errorf("scenario %q expected result must be non-empty", scenario.Title)
			}
		}
	}
	return nil
}

func validateOutcomes(batch *OutcomeBatch, campaign *Campaign) (counts, error) {
	known := make(map[string]struct{})
	for _, axis := range campaign.Axes {
		for _, scenario := range axis.Scenarios {
			known[axis.Title+"\x00"+scenario.Title] = struct{}{}
		}
	}
	if len(batch.Outcomes) == 0 {
		return counts{}, errors.New("outcomes must contain at least one result")
	}
	if len(batch.Outcomes) > MaxOutcomes {
		return counts{}, fmt.Errorf("outcomes contain %d results; maximum is %d", len(batch.Outcomes), MaxOutcomes)
	}
	seen := make(map[string]struct{}, len(batch.Outcomes))
	var total counts
	for _, outcome := range batch.Outcomes {
		if len(outcome.Axis) > MaxTextBytes || len(outcome.Scenario) > MaxTextBytes || len(outcome.Status) > MaxTextBytes || len(outcome.Reason) > MaxTextBytes {
			return counts{}, fmt.Errorf("outcome strings must not exceed %d bytes", MaxTextBytes)
		}
		key := outcome.Axis + "\x00" + outcome.Scenario
		if _, exists := known[key]; !exists {
			return counts{}, fmt.Errorf("unknown campaign scenario %q / %q", outcome.Axis, outcome.Scenario)
		}
		if _, exists := seen[key]; exists {
			return counts{}, fmt.Errorf("duplicate outcome for %q / %q", outcome.Axis, outcome.Scenario)
		}
		seen[key] = struct{}{}
		switch outcome.Status {
		case "pass":
			total.Pass++
		case "finding":
			total.Finding++
		case "not_exercised":
			total.NotExercised++
		default:
			return counts{}, fmt.Errorf("invalid outcome status %q", outcome.Status)
		}
	}
	return total, nil
}

func validateLedger(value *ledger) error {
	if len(value.Passes) > MaxPasses {
		return fmt.Errorf("ledger has %d passes; maximum is %d", len(value.Passes), MaxPasses)
	}
	seenPasses := make(map[string]struct{}, len(value.Passes))
	for _, pass := range value.Passes {
		if len(pass.PassID) > MaxIDBytes {
			return fmt.Errorf("ledger pass ID exceeds %d bytes", MaxIDBytes)
		}
		if pass.PassID == "" {
			return errors.New("ledger pass ID must be non-empty")
		}
		if _, exists := seenPasses[pass.PassID]; exists {
			return fmt.Errorf("duplicate ledger pass %q", pass.PassID)
		}
		seenPasses[pass.PassID] = struct{}{}
		if _, err := parsePassKind(string(pass.Kind)); err != nil {
			return err
		}
		if pass.Kind == passWave && pass.Wave <= 0 {
			return fmt.Errorf("wave pass %q has invalid wave %d", pass.PassID, pass.Wave)
		}
		if len(pass.Outcomes) == 0 {
			return fmt.Errorf("pass %q has no outcomes", pass.PassID)
		}
		if len(pass.Outcomes) > MaxOutcomes {
			return fmt.Errorf("pass %q has %d outcomes; maximum is %d", pass.PassID, len(pass.Outcomes), MaxOutcomes)
		}
		seenOutcomes := make(map[string]struct{}, len(pass.Outcomes))
		var computed counts
		for _, outcome := range pass.Outcomes {
			if len(outcome.Axis) > MaxTextBytes || len(outcome.Scenario) > MaxTextBytes || len(outcome.Status) > MaxTextBytes || len(outcome.Reason) > MaxTextBytes {
				return fmt.Errorf("pass %q has an outcome string exceeding %d bytes", pass.PassID, MaxTextBytes)
			}
			if outcome.Axis == "" || outcome.Scenario == "" {
				return fmt.Errorf("pass %q has an outcome with an empty axis or scenario", pass.PassID)
			}
			key := outcomeKey(outcome.Axis, outcome.Scenario)
			if _, exists := seenOutcomes[key]; exists {
				return fmt.Errorf("pass %q has duplicate outcome %q / %q", pass.PassID, outcome.Axis, outcome.Scenario)
			}
			seenOutcomes[key] = struct{}{}
			switch outcome.Status {
			case "pass":
				computed.Pass++
			case "finding":
				computed.Finding++
			case "not_exercised":
				computed.NotExercised++
			default:
				return fmt.Errorf("pass %q has invalid outcome status %q", pass.PassID, outcome.Status)
			}
		}
		if computed != pass.Counts {
			return fmt.Errorf("pass %q counts are %+v; computed %+v", pass.PassID, pass.Counts, computed)
		}
	}
	return nil
}
