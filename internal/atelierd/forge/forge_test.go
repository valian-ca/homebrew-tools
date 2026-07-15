package forge

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/outbox"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/paths"
)

func startTestRun(t *testing.T, cap int) string {
	t.Helper()
	runID, err := Start("VAL-306", "session-306", cap)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return runID
}

func writeTestFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func copyFixture(t *testing.T, name, target string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	if err := os.WriteFile(target, data, 0o600); err != nil {
		t.Fatalf("write fixture %s: %v", name, err)
	}
}

func TestRunIsolationStateAndModes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	first := startTestRun(t, DefaultCap)
	campaign := writeTestFile(t, "campaign.json", `{"schemaVersion":1,"axes":[{"title":"A","scenarios":[{"title":"S","steps":["Do"],"expected":"Done"}]}]}`)
	if err := SaveCampaign(first, campaign); err != nil {
		t.Fatalf("SaveCampaign: %v", err)
	}
	second := startTestRun(t, DefaultCap)
	if first == second {
		t.Fatal("fresh starts returned the same run ID")
	}
	if _, err := os.Stat(paths.ForgeCampaign(second)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("second campaign exists or stat failed: %v", err)
	}
	if _, err := os.Stat(paths.ForgeLedger(second)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("second ledger exists or stat failed: %v", err)
	}
	if !strings.HasPrefix(paths.ForgeRun(second), filepath.Join(home, ".atelier", "forge")+string(os.PathSeparator)) {
		t.Fatalf("run state escaped forge root: %s", paths.ForgeRun(second))
	}
	state, err := os.Stat(paths.ForgeRunState(second))
	if err != nil {
		t.Fatalf("stat run state: %v", err)
	}
	if state.Mode().Perm() != 0o600 {
		t.Fatalf("run state mode = %o, want 600", state.Mode().Perm())
	}
	dir, err := os.Stat(paths.ForgeRun(second))
	if err != nil {
		t.Fatalf("stat run dir: %v", err)
	}
	if dir.Mode().Perm() != 0o700 {
		t.Fatalf("run dir mode = %o, want 700", dir.Mode().Perm())
	}
	status, err := Status(second)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Wave != 0 || status.OpenPass != "" || status.Refs.Report != "" || status.Refs.Testplan != "" {
		t.Fatalf("fresh status = %+v", status)
	}
}

func TestStartPrunesOldRuns(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	oldID := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	if err := ensureDir(paths.ForgeRun(oldID)); err != nil {
		t.Fatalf("ensure old run: %v", err)
	}
	if err := os.WriteFile(paths.ForgeRunLock(oldID), nil, 0o600); err != nil {
		t.Fatalf("write old lock: %v", err)
	}
	old := time.Now().Add(-retention - time.Hour)
	if err := os.Chtimes(paths.ForgeRun(oldID), old, old); err != nil {
		t.Fatalf("age old run: %v", err)
	}
	startTestRun(t, DefaultCap)
	if _, err := os.Stat(paths.ForgeRun(oldID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old run was not pruned: %v", err)
	}
}

func TestWaveAndPassConcurrency(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runID := startTestRun(t, 1)
	campaign := writeTestFile(t, "campaign.json", `{"schemaVersion":1,"axes":[{"title":"A","scenarios":[{"title":"S","steps":["Do"],"expected":"Done"}]}]}`)
	if err := SaveCampaign(runID, campaign); err != nil {
		t.Fatalf("SaveCampaign: %v", err)
	}
	const workers = 12
	var wg sync.WaitGroup
	waveResults := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := OpenWave(runID)
			waveResults <- err
		}()
	}
	wg.Wait()
	close(waveResults)
	waveSuccess := 0
	for err := range waveResults {
		if err == nil {
			waveSuccess++
		} else if !errors.Is(err, ErrInvalidPass) {
			t.Fatalf("concurrent OpenWave: %v", err)
		}
	}
	if waveSuccess != 1 {
		t.Fatalf("successful concurrent wave opens = %d, want 1", waveSuccess)
	}
	passResults := make(chan struct {
		path string
		err  error
	}, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			path, err := NextPass(runID, "wave")
			passResults <- struct {
				path string
				err  error
			}{path, err}
		}()
	}
	wg.Wait()
	close(passResults)
	pathsSeen := map[string]struct{}{}
	for result := range passResults {
		if result.err == nil {
			pathsSeen[result.path] = struct{}{}
		} else if !errors.Is(result.err, ErrInvalidPass) {
			t.Fatalf("concurrent NextPass: %v", result.err)
		}
	}
	if len(pathsSeen) != 1 {
		t.Fatalf("unique successful wave pass dirs = %d, want 1", len(pathsSeen))
	}
	outcome := writeTestFile(t, "outcome.json", `{"schemaVersion":1,"outcomes":[{"axis":"A","scenario":"S","status":"finding"}]}`)
	if err := RecordOutcome(runID, "wave-1", outcome); err != nil {
		t.Fatalf("RecordOutcome: %v", err)
	}
	if decision, err := CloseWave(runID, 1); err != nil || decision != "cap" {
		t.Fatalf("CloseWave = %q, %v; want cap", decision, err)
	}
	if _, err := OpenWave(runID); !errors.Is(err, ErrWaveCap) {
		t.Fatalf("OpenWave at cap error = %v", err)
	}
}

func TestPassSequencesAndOutcomeCounts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runID := startTestRun(t, 2)
	campaign := writeTestFile(t, "campaign.json", `{"schemaVersion":1,"axes":[{"title":"A","scenarios":[{"title":"S","steps":["Do"],"expected":"Done"}]}]}`)
	outcome := writeTestFile(t, "outcome.json", `{"schemaVersion":1,"outcomes":[{"axis":"A","scenario":"S","status":"finding","reason":"broken"}]}`)
	if err := SaveCampaign(runID, campaign); err != nil {
		t.Fatalf("SaveCampaign: %v", err)
	}
	if _, err := NextPass(runID, "wave"); !errors.Is(err, ErrInvalidPass) {
		t.Fatalf("wave pass without wave error = %v", err)
	}
	if _, err := OpenWave(runID); err != nil {
		t.Fatalf("OpenWave: %v", err)
	}
	wavePath, err := NextPass(runID, "wave")
	if err != nil {
		t.Fatalf("NextPass wave: %v", err)
	}
	if filepath.Base(wavePath) != "wave-1" {
		t.Fatalf("wave path = %s", wavePath)
	}
	if err := RecordOutcome(runID, "wave-1", outcome); err != nil {
		t.Fatalf("RecordOutcome wave: %v", err)
	}
	if decision, err := CloseWave(runID, 1); err != nil || decision != "continue" {
		t.Fatalf("CloseWave = %q, %v; want continue", decision, err)
	}
	for _, kind := range []string{"review", "review", "repair", "repair"} {
		path, err := NextPass(runID, kind)
		if err != nil {
			t.Fatalf("NextPass %s: %v", kind, err)
		}
		if err := RecordOutcome(runID, filepath.Base(path), outcome); err != nil {
			t.Fatalf("RecordOutcome %s: %v", kind, err)
		}
	}
	state, err := readRun(runID)
	if err != nil {
		t.Fatalf("readRun: %v", err)
	}
	want := []string{"wave-1", "review-1", "review-2", "repair-1", "repair-2"}
	for i, pass := range state.Passes {
		if pass.ID != want[i] {
			t.Fatalf("pass %d = %s, want %s", i, pass.ID, want[i])
		}
	}
	ledger, err := readLedger(runID)
	if err != nil {
		t.Fatalf("readLedger: %v", err)
	}
	for _, pass := range ledger.Passes {
		if pass.Counts != (counts{Finding: 1}) {
			t.Fatalf("counts for %s = %+v", pass.PassID, pass.Counts)
		}
	}
	for _, dir := range []string{paths.Forge(), paths.ForgeRun(runID), paths.ForgeCaptures(runID), wavePath} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat dir %s: %v", dir, err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("dir %s mode = %o, want 700", dir, info.Mode().Perm())
		}
	}
	for _, file := range []string{paths.ForgeRunState(runID), paths.ForgeRunLock(runID), paths.ForgeCampaign(runID), paths.ForgeLedger(runID)} {
		info, err := os.Stat(file)
		if err != nil {
			t.Fatalf("stat file %s: %v", file, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("file %s mode = %o, want 600", file, info.Mode().Perm())
		}
	}
}

func TestCloseWaveRequiresRecordedPassAndMatchingFindings(t *testing.T) {
	setup := func(t *testing.T) (string, string) {
		t.Helper()
		t.Setenv("HOME", t.TempDir())
		runID := startTestRun(t, 1)
		campaign := writeTestFile(t, "campaign.json", `{"schemaVersion":1,"axes":[{"title":"A","scenarios":[{"title":"S","steps":["Do"],"expected":"Done"}]}]}`)
		if err := SaveCampaign(runID, campaign); err != nil {
			t.Fatalf("SaveCampaign: %v", err)
		}
		if _, err := OpenWave(runID); err != nil {
			t.Fatalf("OpenWave: %v", err)
		}
		outcome := writeTestFile(t, "outcome.json", `{"schemaVersion":1,"outcomes":[{"axis":"A","scenario":"S","status":"finding"}]}`)
		return runID, outcome
	}

	t.Run("no pass", func(t *testing.T) {
		runID, _ := setup(t)
		if _, err := CloseWave(runID, 0); !errors.Is(err, ErrInvalidPass) {
			t.Fatalf("CloseWave error = %v", err)
		}
	})

	t.Run("open pass", func(t *testing.T) {
		runID, _ := setup(t)
		if _, err := NextPass(runID, "wave"); err != nil {
			t.Fatalf("NextPass: %v", err)
		}
		if _, err := CloseWave(runID, 0); !errors.Is(err, ErrInvalidPass) {
			t.Fatalf("CloseWave error = %v", err)
		}
	})

	t.Run("finding mismatch", func(t *testing.T) {
		runID, outcome := setup(t)
		if _, err := NextPass(runID, "wave"); err != nil {
			t.Fatalf("NextPass: %v", err)
		}
		if err := RecordOutcome(runID, "wave-1", outcome); err != nil {
			t.Fatalf("RecordOutcome: %v", err)
		}
		if _, err := CloseWave(runID, 0); !errors.Is(err, ErrInvalidPass) {
			t.Fatalf("CloseWave error = %v", err)
		}
		state, err := readRun(runID)
		if err != nil {
			t.Fatalf("readRun: %v", err)
		}
		if !state.WaveOpen {
			t.Fatal("finding mismatch closed the wave")
		}
	})

	t.Run("valid close", func(t *testing.T) {
		runID, outcome := setup(t)
		if _, err := NextPass(runID, "wave"); err != nil {
			t.Fatalf("NextPass: %v", err)
		}
		if err := RecordOutcome(runID, "wave-1", outcome); err != nil {
			t.Fatalf("RecordOutcome: %v", err)
		}
		if decision, err := CloseWave(runID, 1); err != nil || decision != "cap" {
			t.Fatalf("CloseWave = %q, %v; want cap", decision, err)
		}
	})
}

func TestRecordOutcomeRecoversStaleOpenPass(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runID := startTestRun(t, DefaultCap)
	campaign := writeTestFile(t, "campaign.json", `{"schemaVersion":1,"axes":[{"title":"A","scenarios":[{"title":"S","steps":["Do"],"expected":"Done"}]}]}`)
	outcome := writeTestFile(t, "outcome.json", `{"schemaVersion":1,"outcomes":[{"axis":"A","scenario":"S","status":"pass"}]}`)
	if err := SaveCampaign(runID, campaign); err != nil {
		t.Fatalf("SaveCampaign: %v", err)
	}
	if _, err := OpenWave(runID); err != nil {
		t.Fatalf("OpenWave: %v", err)
	}
	if _, err := NextPass(runID, "wave"); err != nil {
		t.Fatalf("NextPass: %v", err)
	}
	if err := withRunLock(runID, func() error {
		return writeJSON(paths.ForgeLedger(runID), &ledger{
			SchemaVersion: SchemaVersion,
			Passes: []ledgerPass{{
				PassID: "wave-1",
				Kind:   passWave,
				Wave:   1,
				Outcomes: []Outcome{{
					Axis: "A", Scenario: "S", Status: "pass",
				}},
				Counts: counts{Pass: 1},
			}},
		})
	}); err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	if err := RecordOutcome(runID, "wave-1", outcome); err != nil {
		t.Fatalf("RecordOutcome retry: %v", err)
	}
	state, err := readRun(runID)
	if err != nil {
		t.Fatalf("readRun: %v", err)
	}
	if state.OpenPass != "" {
		t.Fatalf("OpenPass = %q, want empty", state.OpenPass)
	}
	ledger, err := readLedger(runID)
	if err != nil {
		t.Fatalf("readLedger: %v", err)
	}
	if len(ledger.Passes) != 1 {
		t.Fatalf("ledger passes = %d, want 1", len(ledger.Passes))
	}
	if err := RecordOutcome(runID, "wave-1", outcome); !errors.Is(err, ErrInvalidPass) {
		t.Fatalf("second duplicate error = %v", err)
	}
	ledger, err = readLedger(runID)
	if err != nil {
		t.Fatalf("readLedger after duplicate: %v", err)
	}
	if len(ledger.Passes) != 1 {
		t.Fatalf("ledger passes after duplicate = %d, want 1", len(ledger.Passes))
	}
}

func TestCampaignFreezesAfterPassAllocation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runID := startTestRun(t, DefaultCap)
	initial := writeTestFile(t, "initial.json", `{"schemaVersion":1,"axes":[{"title":"A","scenarios":[{"title":"S","steps":["Do"],"expected":"Done"}]}]}`)
	if err := SaveCampaign(runID, initial); err != nil {
		t.Fatalf("SaveCampaign: %v", err)
	}
	if _, err := OpenWave(runID); err != nil {
		t.Fatalf("OpenWave: %v", err)
	}
	if _, err := NextPass(runID, "wave"); err != nil {
		t.Fatalf("NextPass: %v", err)
	}
	tests := []struct {
		name string
		body string
	}{
		{"scenario deletion", `{"schemaVersion":1,"axes":[{"title":"A","scenarios":[{"title":"T","steps":["Do"],"expected":"Done"}]}]}`},
		{"expected rewrite", `{"schemaVersion":1,"axes":[{"title":"A","scenarios":[{"title":"S","steps":["Do"],"expected":"Changed"}]}]}`},
	}
	for _, test := range tests {
		replacement := writeTestFile(t, test.name+".json", test.body)
		if err := SaveCampaign(runID, replacement); !errors.Is(err, ErrInvalidStaging) {
			t.Fatalf("%s error = %v", test.name, err)
		}
	}
}

func TestGoldenSummaryAndFrenchTestplan(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runID := startTestRun(t, DefaultCap)
	copyFixture(t, "campaign.json", paths.ForgeCampaign(runID))
	copyFixture(t, "ledger.json", paths.ForgeLedger(runID))
	summary, err := Summary(runID)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	wantSummary, err := os.ReadFile(filepath.Join("testdata", "summary.golden"))
	if err != nil {
		t.Fatalf("read summary golden: %v", err)
	}
	if summary != string(wantSummary) {
		t.Fatalf("summary mismatch\n--- got ---\n%s--- want ---\n%s", summary, wantSummary)
	}
	testplan, _, err := RenderTestplan(runID, "fr", "")
	if err != nil {
		t.Fatalf("RenderTestplan: %v", err)
	}
	wantTestplan, err := os.ReadFile(filepath.Join("testdata", "testplan_fr.golden"))
	if err != nil {
		t.Fatalf("read testplan golden: %v", err)
	}
	if testplan != string(wantTestplan) {
		t.Fatalf("testplan mismatch\n--- got ---\n%s--- want ---\n%s", testplan, wantTestplan)
	}
}

func TestRenderRejectsForgeOutputPaths(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runID := startTestRun(t, DefaultCap)
	tests := []struct {
		name   string
		target func(t *testing.T) string
	}{
		{
			name: "direct protected file",
			target: func(_ *testing.T) string {
				return paths.ForgeRunState(runID)
			},
		},
		{
			name: "symlinked parent",
			target: func(t *testing.T) string {
				parent := t.TempDir()
				link := filepath.Join(parent, "run-link")
				if err := os.Symlink(paths.ForgeRun(runID), link); err != nil {
					t.Fatalf("Symlink: %v", err)
				}
				return filepath.Join(link, "plan.md")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := test.target(t)
			_, _, err := RenderTestplan(runID, "en", target)
			if err == nil || !strings.Contains(err.Error(), "must not be inside") {
				t.Fatalf("RenderTestplan error = %v", err)
			}
			if errors.Is(err, ErrUnknownRun) || errors.Is(err, ErrInvalidPass) || errors.Is(err, ErrCampaignInvalid) || errors.Is(err, ErrWaveCap) || errors.Is(err, ErrInvalidStaging) {
				t.Fatalf("protected output used forge sentinel: %v", err)
			}
		})
	}
	if _, err := Status(runID); err != nil {
		t.Fatalf("protected run state was modified: %v", err)
	}
}

func TestSchemaMismatchClassification(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runID := startTestRun(t, DefaultCap)
	copyFixture(t, "campaign.json", paths.ForgeCampaign(runID))
	copyFixture(t, "ledger.json", paths.ForgeLedger(runID))
	tests := []struct {
		name             string
		path             string
		load             func() error
		campaignSentinel bool
	}{
		{"run", paths.ForgeRunState(runID), func() error { _, err := Status(runID); return err }, false},
		{"campaign", paths.ForgeCampaign(runID), func() error { _, err := LoadCampaign(runID); return err }, true},
		{"ledger", paths.ForgeLedger(runID), func() error { _, err := Summary(runID); return err }, false},
	}
	for _, test := range tests {
		data, err := os.ReadFile(test.path)
		if err != nil {
			t.Fatalf("read %s: %v", test.name, err)
		}
		data = []byte(strings.Replace(string(data), `"schemaVersion": 1`, `"schemaVersion": 2`, 1))
		if err := os.WriteFile(test.path, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", test.name, err)
		}
		err = test.load()
		if err == nil {
			t.Fatalf("%s schema mismatch error = %v", test.name, err)
		}
		if errors.Is(err, ErrCampaignInvalid) != test.campaignSentinel {
			t.Fatalf("%s campaign sentinel = %v, want %v: %v", test.name, errors.Is(err, ErrCampaignInvalid), test.campaignSentinel, err)
		}
		if errors.Is(err, ErrUnknownRun) || errors.Is(err, ErrInvalidPass) || errors.Is(err, ErrWaveCap) || errors.Is(err, ErrInvalidStaging) {
			t.Fatalf("%s schema mismatch used wrong sentinel: %v", test.name, err)
		}
		data = []byte(strings.Replace(string(data), `"schemaVersion": 2`, `"schemaVersion": 1`, 1))
		if err := os.WriteFile(test.path, data, 0o600); err != nil {
			t.Fatalf("restore %s: %v", test.name, err)
		}
	}
}

func TestEventPayloadsAndPersistedSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runID := startTestRun(t, 1)
	campaign := writeTestFile(t, "campaign.json", `{"schemaVersion":1,"axes":[{"title":"A","scenarios":[{"title":"S","steps":["Do"],"expected":"Done"}]}]}`)
	if err := SaveCampaign(runID, campaign); err != nil {
		t.Fatalf("SaveCampaign: %v", err)
	}
	if _, err := OpenWave(runID); err != nil {
		t.Fatalf("OpenWave: %v", err)
	}
	if _, err := NextPass(runID, "wave"); err != nil {
		t.Fatalf("NextPass: %v", err)
	}
	outcome := writeTestFile(t, "outcome.json", `{"schemaVersion":1,"outcomes":[{"axis":"A","scenario":"S","status":"pass"}]}`)
	if err := RecordOutcome(runID, "wave-1", outcome); err != nil {
		t.Fatalf("RecordOutcome: %v", err)
	}
	if _, err := CloseWave(runID, 0); err != nil {
		t.Fatalf("CloseWave: %v", err)
	}
	if err := SetRef(runID, "report", "comment-42"); err != nil {
		t.Fatalf("SetRef: %v", err)
	}
	if _, _, err := RenderTestplan(runID, "en", ""); err != nil {
		t.Fatalf("RenderTestplan: %v", err)
	}
	files, err := outbox.List()
	if err != nil {
		t.Fatalf("outbox.List: %v", err)
	}
	wantTypes := []string{
		"forge:run-start",
		"forge:campaign-saved",
		"forge:wave-open",
		"forge:pass",
		"forge:wave-close",
		"forge:report-linked",
		"forge:testplan-published",
	}
	wantPayloads := []map[string]any{
		{"runId": runID, "ticket": "VAL-306", "cap": float64(1)},
		{"runId": runID, "axes": float64(1), "scenarios": float64(1)},
		{"runId": runID, "wave": float64(1)},
		{"runId": runID, "passId": "wave-1", "kind": "wave", "wave": float64(1)},
		{"runId": runID, "wave": float64(1), "findings": float64(0), "decision": "dry"},
		{"runId": runID, "report": "comment-42"},
		{"runId": runID, "language": "en", "path": ""},
	}
	if len(files) != len(wantTypes) {
		t.Fatalf("events = %d, want %d", len(files), len(wantTypes))
	}
	for i, file := range files {
		envelope, err := outbox.Read(file)
		if err != nil {
			t.Fatalf("read event: %v", err)
		}
		if envelope.Type != wantTypes[i] || envelope.ClaudeSessionID != "session-306" || !reflect.DeepEqual(envelope.Payload, wantPayloads[i]) {
			t.Fatalf("event %d = %+v", i, envelope)
		}
	}
}

func TestInvalidStagingAndCampaignErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runID := startTestRun(t, DefaultCap)
	if _, err := LoadCampaign(runID); !errors.Is(err, ErrCampaignInvalid) {
		t.Fatalf("absent campaign error = %v", err)
	}
	invalid := writeTestFile(t, "invalid.json", `{"schemaVersion":1,"axes":[]}`)
	if err := SaveCampaign(runID, invalid); !errors.Is(err, ErrInvalidStaging) {
		t.Fatalf("invalid staging error = %v", err)
	}
	if _, err := Status("not-a-run"); !errors.Is(err, ErrUnknownRun) {
		t.Fatalf("unknown run error = %v", err)
	}
}

func TestLedgerRejectsForgedCounts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runID := startTestRun(t, DefaultCap)
	copyFixture(t, "campaign.json", paths.ForgeCampaign(runID))
	copyFixture(t, "ledger.json", paths.ForgeLedger(runID))
	data, err := os.ReadFile(paths.ForgeLedger(runID))
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	data = []byte(strings.Replace(string(data), `"finding": 1`, `"finding": 99`, 1))
	if err := os.WriteFile(paths.ForgeLedger(runID), data, 0o600); err != nil {
		t.Fatalf("write ledger: %v", err)
	}
	if _, err := Summary(runID); err == nil || !strings.Contains(err.Error(), "computed") {
		t.Fatalf("forged ledger error = %v", err)
	}
}

func TestForgePackageHasNoFirestoreImport(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	forbidden := "internal/atelierd/" + "firestore"
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(entry.Name())
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		if strings.Contains(string(data), forbidden) {
			t.Errorf("%s imports %s", entry.Name(), forbidden)
		}
	}
}

func TestStatusJSONIsOneLine(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runID := startTestRun(t, DefaultCap)
	value, err := StatusJSON(runID)
	if err != nil {
		t.Fatalf("StatusJSON: %v", err)
	}
	if strings.Contains(string(value), "\n") {
		t.Fatalf("status contains newline: %q", value)
	}
	want := fmt.Sprintf(`{"schemaVersion":1,"runId":%q,"ticket":"VAL-306","wave":0,"waveOpen":false,"openPass":"","refs":{"report":"","testplan":""}}`, runID)
	if string(value) != want {
		t.Fatalf("status = %s, want %s", value, want)
	}
}
