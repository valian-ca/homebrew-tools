package forge

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/events"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/paths"
)

type latestOutcome struct {
	Outcome
	PassID string
}

type labels struct {
	title      string
	steps      string
	expected   string
	latest     string
	pass       string
	reason     string
	unrecorded string
	colon      string
	statuses   map[string]string
}

var testplanLabels = map[string]labels{
	"en": {
		title: "Test plan", steps: "Steps", expected: "Expected", latest: "Latest outcome",
		pass: "Pass", reason: "Reason", unrecorded: "Not recorded",
		colon:    ":",
		statuses: map[string]string{"pass": "Passed", "finding": "Finding", "not_exercised": "Not exercised"},
	},
	"fr": {
		title: "Plan de tests", steps: "Étapes", expected: "Résultat attendu", latest: "Dernier résultat",
		pass: "Passe", reason: "Motif", unrecorded: "Non enregistré",
		colon:    " :",
		statuses: map[string]string{"pass": "Réussi", "finding": "Problème", "not_exercised": "Non exercé"},
	},
}

func outcomeKey(axis, scenario string) string {
	return axis + "\x00" + scenario
}

func latestOutcomes(value *ledger) map[string]latestOutcome {
	latest := make(map[string]latestOutcome)
	for _, pass := range value.Passes {
		for _, outcome := range pass.Outcomes {
			latest[outcomeKey(outcome.Axis, outcome.Scenario)] = latestOutcome{Outcome: outcome, PassID: pass.PassID}
		}
	}
	return latest
}

func Summary(runID string) (string, error) {
	var result string
	err := withRunLock(runID, func() error {
		if _, err := readRun(runID); err != nil {
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
		var builder strings.Builder
		builder.WriteString("## Forge counts\n")
		latestWave := -1
		for i := range ledger.Passes {
			if ledger.Passes[i].Kind == passWave {
				latestWave = i
			}
		}
		if latestWave == -1 {
			builder.WriteString("- Latest normal wave: none\n")
		} else {
			pass := ledger.Passes[latestWave]
			fmt.Fprintf(&builder, "- Latest normal wave: %s — pass: %d, finding: %d, not_exercised: %d\n",
				pass.PassID, pass.Counts.Pass, pass.Counts.Finding, pass.Counts.NotExercised)
		}
		latest := latestOutcomes(ledger)
		var rollup counts
		unrecorded := 0
		for _, axis := range campaign.Axes {
			for _, scenario := range axis.Scenarios {
				outcome, ok := latest[outcomeKey(axis.Title, scenario.Title)]
				if !ok {
					unrecorded++
					continue
				}
				switch outcome.Status {
				case "pass":
					rollup.Pass++
				case "finding":
					rollup.Finding++
				case "not_exercised":
					rollup.NotExercised++
				}
			}
		}
		fmt.Fprintf(&builder, "- Campaign rollup: pass: %d, finding: %d, not_exercised: %d, unrecorded: %d\n",
			rollup.Pass, rollup.Finding, rollup.NotExercised, unrecorded)
		if len(ledger.Passes) == 0 {
			builder.WriteString("- Pass findings: none\n")
		} else {
			builder.WriteString("- Pass findings:\n")
			for _, pass := range ledger.Passes {
				fmt.Fprintf(&builder, "  - %s: %d\n", pass.PassID, pass.Counts.Finding)
			}
		}
		result = builder.String()
		return nil
	})
	return result, err
}

func RenderTestplan(runID, language, outputPath string) (string, string, error) {
	label, ok := testplanLabels[language]
	if !ok {
		return "", "", fmt.Errorf("unsupported testplan language %q", language)
	}
	if err := validateTestplanOutputPath(outputPath); err != nil {
		return "", "", err
	}
	var content string
	var state *runState
	err := withRunLock(runID, func() error {
		var err error
		state, err = readRun(runID)
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
		latest := latestOutcomes(ledger)
		var builder strings.Builder
		fmt.Fprintf(&builder, "# %s — %s\n", label.title, state.Ticket)
		for _, axis := range campaign.Axes {
			fmt.Fprintf(&builder, "\n## %s\n", axis.Title)
			for _, scenario := range axis.Scenarios {
				fmt.Fprintf(&builder, "\n### %s\n", scenario.Title)
				fmt.Fprintf(&builder, "- %s%s\n", label.steps, label.colon)
				for i, step := range scenario.Steps {
					fmt.Fprintf(&builder, "  %d. %s\n", i+1, step)
				}
				fmt.Fprintf(&builder, "- %s%s %s\n", label.expected, label.colon, scenario.Expected)
				outcome, found := latest[outcomeKey(axis.Title, scenario.Title)]
				if !found {
					fmt.Fprintf(&builder, "- %s%s %s\n", label.latest, label.colon, label.unrecorded)
					continue
				}
				fmt.Fprintf(&builder, "- %s%s %s\n", label.latest, label.colon, label.statuses[outcome.Status])
				fmt.Fprintf(&builder, "- %s%s %s\n", label.pass, label.colon, outcome.PassID)
				if outcome.Reason != "" {
					fmt.Fprintf(&builder, "- %s%s %s\n", label.reason, label.colon, outcome.Reason)
				}
			}
		}
		content = builder.String()
		if outputPath != "" {
			return writeBytes(outputPath, []byte(content))
		}
		return nil
	})
	if err != nil {
		return "", "", err
	}
	eventPath := outputPath
	if eventPath != "" {
		if absolute, err := filepath.Abs(eventPath); err == nil {
			eventPath = absolute
		}
	}
	if err := emit(state, events.ForgeTestplanPublished, map[string]any{
		"language": language, "path": eventPath,
	}); err != nil {
		return "", "", err
	}
	return content, outputPath, nil
}

func validateTestplanOutputPath(outputPath string) error {
	if outputPath == "" {
		return nil
	}
	outputAbsolute, err := filepath.Abs(outputPath)
	if err != nil {
		return fmt.Errorf("resolve testplan output: %w", err)
	}
	forgeAbsolute, err := filepath.Abs(paths.Forge())
	if err != nil {
		return fmt.Errorf("resolve forge root: %w", err)
	}
	if pathWithin(outputAbsolute, forgeAbsolute) {
		return fmt.Errorf("testplan output must not be inside %s", paths.Forge())
	}
	resolvedOutput, err := resolveExistingPath(outputAbsolute)
	if err != nil {
		return fmt.Errorf("resolve testplan output: %w", err)
	}
	resolvedForge, err := resolveExistingPath(forgeAbsolute)
	if err != nil {
		return fmt.Errorf("resolve forge root: %w", err)
	}
	if pathWithin(resolvedOutput, resolvedForge) {
		return fmt.Errorf("testplan output must not be inside %s", paths.Forge())
	}
	return nil
}

func resolveExistingPath(path string) (string, error) {
	current := filepath.Clean(path)
	var suffix []string
	for {
		if _, err := os.Lstat(current); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("no existing ancestor for %s", path)
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
	resolved, err := filepath.EvalSymlinks(current)
	if err != nil {
		return "", err
	}
	for i := len(suffix) - 1; i >= 0; i-- {
		resolved = filepath.Join(resolved, suffix[i])
	}
	return filepath.Clean(resolved), nil
}

func pathWithin(path, root string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}
