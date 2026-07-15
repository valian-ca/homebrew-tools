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

	"github.com/gofrs/flock"

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

func TestStartRejectsSymlinkedForgeRoot(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.Symlink(outside, filepath.Join(home, ".atelier")); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(outside)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Start("VAL-306", "session-306", DefaultCap); err == nil {
		t.Fatal("Start unexpectedly followed symlinked forge root")
	}
	if _, err := FindRun("VAL-306", ""); err == nil {
		t.Fatal("FindRun unexpectedly followed symlinked forge root")
	}
	after, err := os.Stat(outside)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) || before.Mode().Perm() != after.Mode().Perm() {
		t.Fatalf("symlink target changed: before=%v after=%v", before, after)
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("Start wrote through forge-root symlink: %v", entries)
	}
}

func TestRunLockRejectsSymlinkedRunDir(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	t.Setenv("HOME", home)
	if err := ensureDir(paths.Forge()); err != nil {
		t.Fatal(err)
	}
	runID := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	if err := os.Symlink(outside, paths.ForgeRun(runID)); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(outside, "marker")
	if err := os.WriteFile(marker, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(outside)
	if err != nil {
		t.Fatal(err)
	}
	if err := withRunLock(runID, func() error {
		return os.WriteFile(filepath.Join(paths.ForgeRun(runID), "escaped"), []byte("bad"), 0o600)
	}); err == nil {
		t.Fatal("withRunLock unexpectedly followed symlinked run directory")
	}
	after, err := os.Stat(outside)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) || before.Mode().Perm() != after.Mode().Perm() {
		t.Fatalf("symlink target changed: before=%v after=%v", before, after)
	}
	if _, err := os.Stat(filepath.Join(outside, "escaped")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("withRunLock wrote through run symlink: %v", err)
	}
}

func TestRunLockRejectsSymlinkedForgeDirBeforeMutation(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.Mkdir(filepath.Join(home, ".atelier"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, paths.Forge()); err != nil {
		t.Fatal(err)
	}
	runID := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	if err := os.Mkdir(filepath.Join(outside, runID), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ShowPass(runID, "wave-1", "", 0); err == nil {
		t.Fatal("ShowPass unexpectedly followed symlinked forge directory")
	}
	if _, err := os.Stat(filepath.Join(outside, runID, "run.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ShowPass mutated symlink target: %v", err)
	}
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

func TestFindRunByExactTicketAndSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	first, err := Start("VAL-307", "session-a", DefaultCap)
	if err != nil {
		t.Fatalf("Start first: %v", err)
	}
	second, err := Start("VAL-308", "session-b", DefaultCap)
	if err != nil {
		t.Fatalf("Start second: %v", err)
	}

	for _, test := range []struct {
		name    string
		ticket  string
		session string
		want    string
	}{
		{name: "ticket", ticket: " VAL-307 ", want: first},
		{name: "session", session: "session-b", want: second},
		{name: "both", ticket: "VAL-308", session: "session-b", want: second},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := FindRun(test.ticket, test.session)
			if err != nil || got != test.want {
				t.Fatalf("FindRun = %q, %v; want %q", got, err, test.want)
			}
		})
	}
	if _, err := FindRun("VAL-999", ""); !errors.Is(err, ErrUnknownRun) {
		t.Fatalf("unknown ticket error = %v", err)
	}
	if _, err := FindRun("", ""); err == nil {
		t.Fatal("empty filters unexpectedly succeeded")
	}
	third, err := Start("VAL-307", "session-c", DefaultCap)
	if err != nil {
		t.Fatalf("Start third: %v", err)
	}
	if _, err := FindRun("VAL-307", ""); !errors.Is(err, ErrAmbiguousRun) || !strings.Contains(err.Error(), first) || !strings.Contains(err.Error(), third) {
		t.Fatalf("ambiguous ticket error = %v", err)
	}
	if got, err := FindRun("VAL-307", "session-a"); err != nil || got != first {
		t.Fatalf("disambiguated FindRun = %q, %v; want %q", got, err, first)
	}
}

func TestForgeNamespaceLockBoundsStartAndFind(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runID := startTestRun(t, DefaultCap)
	held := flock.New(paths.ForgeLock())
	locked, err := held.TryLock()
	if err != nil || !locked {
		t.Fatalf("hold forge lock: locked=%v err=%v", locked, err)
	}
	defer func() { _ = held.Unlock() }()

	started := time.Now()
	if _, err := Start("VAL-308", "session-b", DefaultCap); !errors.Is(err, ErrRunBusy) {
		t.Fatalf("Start error = %v, want ErrRunBusy", err)
	}
	if elapsed := time.Since(started); elapsed >= 2*time.Second {
		t.Fatalf("blocked Start took %s", elapsed)
	}
	started = time.Now()
	if _, err := FindRun("VAL-306", ""); !errors.Is(err, ErrRunBusy) {
		t.Fatalf("FindRun error = %v, want ErrRunBusy for %s", err, runID)
	}
	if elapsed := time.Since(started); elapsed >= 2*time.Second {
		t.Fatalf("blocked FindRun took %s", elapsed)
	}
}

func TestShowPassCampaignlessSelectors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runID := startTestRun(t, 2)
	if _, err := OpenWave(runID); err != nil {
		t.Fatalf("OpenWave 1: %v", err)
	}
	waveOneDir, err := NextPass(runID, "wave")
	if err != nil {
		t.Fatalf("NextPass wave 1: %v", err)
	}
	status, err := ShowPass(runID, "", "wave", 0)
	if err != nil {
		t.Fatalf("ShowPass latest wave: %v", err)
	}
	if status.PassID != "wave-1" || status.CaptureDir != waveOneDir || !status.Complete || status.CampaignRequired {
		t.Fatalf("campaignless wave status = %+v", status)
	}
	if exact, err := ShowPass(runID, "wave-1", "", 0); err != nil || exact != status {
		t.Fatalf("ShowPass exact = %+v, %v; want %+v", exact, err, status)
	}
	if decision, err := CloseWave(runID, 1); err != nil || decision != "continue" {
		t.Fatalf("CloseWave 1 = %q, %v", decision, err)
	}
	if _, err := OpenWave(runID); err != nil {
		t.Fatalf("OpenWave 2: %v", err)
	}
	if _, err := ShowPass(runID, "", "wave", 0); !errors.Is(err, ErrInvalidPass) {
		t.Fatalf("current wave without pass error = %v", err)
	}
	waveTwoDir, err := NextPass(runID, "wave")
	if err != nil {
		t.Fatalf("NextPass wave 2: %v", err)
	}
	if latest, err := ShowPass(runID, "", "wave", 0); err != nil || latest.PassID != "wave-2" || latest.CaptureDir != waveTwoDir {
		t.Fatalf("latest wave = %+v, %v", latest, err)
	}
	if first, err := ShowPass(runID, "", "wave", 1); err != nil || first.PassID != "wave-1" || first.CaptureDir != waveOneDir {
		t.Fatalf("wave 1 selector = %+v, %v", first, err)
	}
	if _, err := NextPass(runID, "review"); err != nil {
		t.Fatalf("NextPass review 1: %v", err)
	}
	reviewTwoDir, err := NextPass(runID, "review")
	if err != nil {
		t.Fatalf("NextPass review 2: %v", err)
	}
	if latest, err := ShowPass(runID, "", "review", 0); err != nil || latest.PassID != "review-2" || latest.CaptureDir != reviewTwoDir {
		t.Fatalf("latest review = %+v, %v", latest, err)
	}
	for _, request := range []struct {
		pass string
		kind string
		wave int
	}{
		{pass: "missing"},
		{pass: "wave-1", kind: "wave"},
		{kind: "review", wave: 1},
		{},
	} {
		if _, err := ShowPass(runID, request.pass, request.kind, request.wave); !errors.Is(err, ErrInvalidPass) {
			t.Fatalf("invalid selector %+v error = %v", request, err)
		}
	}
}

func TestShowPassStrictCompletionAndCaptureIntegrity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runID := startTestRun(t, DefaultCap)
	campaign := writeTestFile(t, "campaign.json", `{"schemaVersion":1,"axes":[{"title":"A","scenarios":[{"title":"S","steps":["Do"],"expected":"Done"}]}]}`)
	if err := SaveCampaign(runID, campaign); err != nil {
		t.Fatalf("SaveCampaign: %v", err)
	}
	if _, err := OpenWave(runID); err != nil {
		t.Fatalf("OpenWave: %v", err)
	}
	captureDir, err := NextPass(runID, "wave")
	if err != nil {
		t.Fatalf("NextPass: %v", err)
	}
	state, err := readRun(runID)
	if err != nil {
		t.Fatalf("read legacy state: %v", err)
	}
	state.CampaignRequired = false
	if err := writeJSON(paths.ForgeRunState(runID), state); err != nil {
		t.Fatalf("write legacy state: %v", err)
	}
	open, err := ShowPass(runID, "wave-1", "", 0)
	if err != nil {
		t.Fatalf("ShowPass open: %v", err)
	}
	if !open.CampaignRequired || open.Complete {
		t.Fatalf("strict open pass status = %+v", open)
	}
	outcome := writeTestFile(t, "outcome.json", `{"schemaVersion":1,"outcomes":[{"axis":"A","scenario":"S","status":"pass"}]}`)
	if err := RecordOutcome(runID, "wave-1", outcome); err != nil {
		t.Fatalf("RecordOutcome: %v", err)
	}
	state, err = readRun(runID)
	if err != nil {
		t.Fatalf("read crash-window state: %v", err)
	}
	state.OpenPass = "wave-1"
	if err := writeJSON(paths.ForgeRunState(runID), state); err != nil {
		t.Fatalf("write crash-window state: %v", err)
	}
	complete, err := ShowPass(runID, "wave-1", "", 0)
	if err != nil || !complete.Complete {
		t.Fatalf("completed strict pass = %+v, %v", complete, err)
	}
	if err := writeJSON(paths.ForgeLedger(runID), &ledger{SchemaVersion: SchemaVersion, Passes: []ledgerPass{}}); err != nil {
		t.Fatalf("clear ledger: %v", err)
	}
	state.OpenPass = ""
	if err := writeJSON(paths.ForgeRunState(runID), state); err != nil {
		t.Fatalf("write cleared state: %v", err)
	}
	incomplete, err := ShowPass(runID, "wave-1", "", 0)
	if err != nil || incomplete.Complete {
		t.Fatalf("strict pass without ledger outcome = %+v, %v", incomplete, err)
	}

	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(outside, "wave-1"), 0o700); err != nil {
		t.Fatalf("mkdir outside pass: %v", err)
	}
	if err := os.RemoveAll(filepath.Dir(captureDir)); err != nil {
		t.Fatalf("remove captures dir: %v", err)
	}
	if err := os.Symlink(outside, filepath.Dir(captureDir)); err != nil {
		t.Fatalf("symlink captures dir: %v", err)
	}
	if _, err := ShowPass(runID, "wave-1", "", 0); err == nil || errors.Is(err, ErrInvalidPass) {
		t.Fatalf("symlinked capture dir error = %v", err)
	}
}

func validScenario(number int) Scenario {
	return Scenario{Title: fmt.Sprintf("scenario-%d", number), Steps: []string{"step"}, Expected: "done"}
}

func TestV1CampaignAndOutcomeBoundaries(t *testing.T) {
	for _, test := range []struct {
		name string
		make func() Campaign
		want string
	}{
		{"axes", func() Campaign {
			c := Campaign{Axes: make([]Axis, MaxAxes)}
			for i := range c.Axes {
				c.Axes[i] = Axis{Title: fmt.Sprintf("axis-%d", i), Scenarios: []Scenario{validScenario(i)}}
			}
			return c
		}, ""},
		{"scenarios per axis", func() Campaign {
			c := Campaign{Axes: []Axis{{Title: "axis", Scenarios: make([]Scenario, MaxScenariosPerAxis)}}}
			for i := range c.Axes[0].Scenarios {
				c.Axes[0].Scenarios[i] = validScenario(i)
			}
			return c
		}, ""},
		{"scenarios total", func() Campaign {
			c := Campaign{Axes: make([]Axis, 8)}
			for i := range c.Axes {
				c.Axes[i] = Axis{Title: fmt.Sprintf("axis-%d", i), Scenarios: make([]Scenario, MaxScenarios/8)}
				for j := range c.Axes[i].Scenarios {
					c.Axes[i].Scenarios[j] = validScenario(i*100 + j)
				}
			}
			return c
		}, ""},
		{"steps", func() Campaign {
			steps := make([]string, MaxSteps)
			for i := range steps {
				steps[i] = "step"
			}
			return Campaign{Axes: []Axis{{Title: "axis", Scenarios: []Scenario{{Title: "scenario", Steps: steps, Expected: "done"}}}}}
		}, ""},
		{"text", func() Campaign {
			return Campaign{Axes: []Axis{{Title: strings.Repeat("a", MaxTextBytes), Scenarios: []Scenario{{Title: "scenario", Steps: []string{"step"}, Expected: "done"}}}}}
		}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			campaign := test.make()
			if err := validateCampaign(&campaign); err != nil {
				t.Fatalf("boundary rejected: %v", err)
			}
		})
	}

	tooMany := Campaign{Axes: []Axis{{Title: "axis", Scenarios: make([]Scenario, MaxScenariosPerAxis+1)}}}
	for i := range tooMany.Axes[0].Scenarios {
		tooMany.Axes[0].Scenarios[i] = validScenario(i)
	}
	if err := validateCampaign(&tooMany); err == nil {
		t.Fatal("per-axis over-limit campaign accepted")
	}
}

func TestV1OversizedStagingAndCampaign(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runID := startTestRun(t, DefaultCap)
	staging := filepath.Join(t.TempDir(), "oversized.json")
	file, err := os.Create(staging)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(MaxStagingFileBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := SaveCampaign(runID, staging); !errors.Is(err, ErrInvalidStaging) {
		t.Fatalf("oversized staging error = %v", err)
	}

	if err := os.WriteFile(paths.ForgeCampaign(runID), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err = os.OpenFile(paths.ForgeCampaign(runID), os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(MaxCampaignFileBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCampaign(runID); !errors.Is(err, ErrCampaignInvalid) {
		t.Fatalf("oversized persisted campaign error = %v", err)
	}
	if err := os.WriteFile(paths.ForgeCampaign(runID), []byte(`{"schemaVersion":1,"axes":[{"title":"A","scenarios":[{"title":"S","steps":["Do"],"expected":"Done"}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		path string
		load func() error
	}{
		{"ledger", paths.ForgeLedger(runID), func() error { _, err := Summary(runID); return err }},
		{"run", paths.ForgeRunState(runID), func() error { _, err := Status(runID); return err }},
	} {
		t.Run(test.name, func(t *testing.T) {
			file, err := os.OpenFile(test.path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			limit := int64(MaxRunFileBytes)
			if test.name == "ledger" {
				limit = MaxLedgerFileBytes
			}
			if err := file.Truncate(limit + 1); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			if err := test.load(); err == nil || errors.Is(err, ErrCampaignInvalid) || errors.Is(err, ErrInvalidStaging) {
				t.Fatalf("oversized persisted %s error = %v; want generic", test.name, err)
			}
		})
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

func TestCampaignlessWaveAndPassConcurrency(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runID := startTestRun(t, 1)
	if _, err := OpenWave(runID); err != nil {
		t.Fatalf("OpenWave: %v", err)
	}
	const workers = 12
	var wg sync.WaitGroup
	passResults := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := NextPass(runID, "wave")
			passResults <- err
		}()
	}
	wg.Wait()
	close(passResults)
	passSuccess := 0
	for err := range passResults {
		if err == nil {
			passSuccess++
		} else if !errors.Is(err, ErrInvalidPass) {
			t.Fatalf("concurrent NextPass: %v", err)
		}
	}
	if passSuccess != 1 {
		t.Fatalf("successful concurrent passes = %d, want 1", passSuccess)
	}
	entries, err := os.ReadDir(paths.ForgeCaptures(runID))
	if err != nil {
		t.Fatalf("read captures: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "wave-1" {
		t.Fatalf("capture entries = %v, want wave-1", entries)
	}

	closeResults := make(chan struct {
		decision string
		err      error
	}, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			decision, err := CloseWave(runID, 1)
			closeResults <- struct {
				decision string
				err      error
			}{decision, err}
		}()
	}
	wg.Wait()
	close(closeResults)
	closeSuccess := 0
	for result := range closeResults {
		if result.err == nil {
			closeSuccess++
			if result.decision != "cap" {
				t.Fatalf("CloseWave decision = %q, want cap", result.decision)
			}
		} else if !errors.Is(result.err, ErrInvalidPass) {
			t.Fatalf("concurrent CloseWave: %v", result.err)
		}
	}
	if closeSuccess != 1 {
		t.Fatalf("successful concurrent closes = %d, want 1", closeSuccess)
	}
}

func TestNextPassWithoutCampaign(t *testing.T) {
	for _, kind := range []passKind{passWave, passReview, passRepair} {
		t.Run(string(kind), func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			runID := startTestRun(t, DefaultCap)
			if kind == passWave {
				if _, err := OpenWave(runID); err != nil {
					t.Fatalf("OpenWave: %v", err)
				}
			}
			captureDir, err := NextPass(runID, string(kind))
			if err != nil {
				t.Fatalf("NextPass %s: %v", kind, err)
			}
			if !filepath.IsAbs(captureDir) {
				t.Fatalf("capture dir = %q, want absolute path", captureDir)
			}
			if info, err := os.Stat(captureDir); err != nil || !info.IsDir() {
				t.Fatalf("capture dir stat = %v, %v", info, err)
			}
			state, err := readRun(runID)
			if err != nil {
				t.Fatalf("readRun: %v", err)
			}
			if state.OpenPass != "" {
				t.Fatalf("OpenPass = %q, want empty without campaign", state.OpenPass)
			}
		})
	}
}

func TestNextPassRejectsInvalidCampaignBeforeMutation(t *testing.T) {
	validCampaign := `{"schemaVersion":1,"axes":[{"title":"A","scenarios":[{"title":"S","steps":["Do"],"expected":"Done"}]}]}`
	for _, kind := range []passKind{passWave, passReview, passRepair} {
		t.Run(string(kind), func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			runID := startTestRun(t, DefaultCap)
			if kind == passWave {
				if _, err := OpenWave(runID); err != nil {
					t.Fatalf("OpenWave: %v", err)
				}
			}
			if err := os.WriteFile(paths.ForgeCampaign(runID), []byte(`{"schemaVersion":1,"axes":[]}`), 0o600); err != nil {
				t.Fatalf("write invalid campaign: %v", err)
			}
			before, err := os.ReadFile(paths.ForgeRunState(runID))
			if err != nil {
				t.Fatalf("read state before: %v", err)
			}
			if _, err := NextPass(runID, string(kind)); !errors.Is(err, ErrCampaignInvalid) {
				t.Fatalf("NextPass %s error = %v; want ErrCampaignInvalid", kind, err)
			}
			after, err := os.ReadFile(paths.ForgeRunState(runID))
			if err != nil {
				t.Fatalf("read state after: %v", err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("failed pass changed run state")
			}
			if _, err := os.Stat(paths.ForgeCaptures(runID)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed pass changed captures: %v", err)
			}

			staging := writeTestFile(t, "campaign.json", validCampaign)
			if err := SaveCampaign(runID, staging); err != nil {
				t.Fatalf("SaveCampaign after failed pass: %v", err)
			}
			if _, err := NextPass(runID, string(kind)); err != nil {
				t.Fatalf("NextPass %s after save: %v", kind, err)
			}
		})
	}
}

func TestCampaignlessPassSequenceUsesUniqueCaptureDirs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runID := startTestRun(t, DefaultCap)
	if _, err := OpenWave(runID); err != nil {
		t.Fatalf("OpenWave: %v", err)
	}
	waveDir, err := NextPass(runID, "wave")
	if err != nil {
		t.Fatalf("NextPass wave: %v", err)
	}
	if decision, err := CloseWave(runID, 1); err != nil || decision != "continue" {
		t.Fatalf("CloseWave = %q, %v; want continue", decision, err)
	}
	reviewDir, err := NextPass(runID, "review")
	if err != nil {
		t.Fatalf("NextPass review: %v", err)
	}
	repairDir, err := NextPass(runID, "repair")
	if err != nil {
		t.Fatalf("NextPass repair: %v", err)
	}
	want := map[string]string{
		waveDir:   "wave-1",
		reviewDir: "review-1",
		repairDir: "repair-1",
	}
	if len(want) != 3 {
		t.Fatalf("unique capture dirs = %d, want 3", len(want))
	}
	for captureDir, base := range want {
		if !filepath.IsAbs(captureDir) || filepath.Base(captureDir) != base {
			t.Fatalf("capture dir = %q, want absolute %s path", captureDir, base)
		}
		if info, err := os.Stat(captureDir); err != nil || !info.IsDir() {
			t.Fatalf("capture dir %s stat = %v, %v", captureDir, info, err)
		}
	}
}

func TestCampaignlessCloseWaveDecisions(t *testing.T) {
	for _, test := range []struct {
		name     string
		cap      int
		findings int
		decision string
	}{
		{name: "continue", cap: 2, findings: 1, decision: "continue"},
		{name: "dry", cap: 2, findings: 0, decision: "dry"},
		{name: "cap", cap: 1, findings: 1, decision: "cap"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			runID := startTestRun(t, test.cap)
			if _, err := OpenWave(runID); err != nil {
				t.Fatalf("OpenWave: %v", err)
			}
			if _, err := NextPass(runID, "wave"); err != nil {
				t.Fatalf("NextPass: %v", err)
			}
			if decision, err := CloseWave(runID, test.findings); err != nil || decision != test.decision {
				t.Fatalf("CloseWave = %q, %v; want %s", decision, err, test.decision)
			}
		})
	}
}

func TestCloseWaveRejectsInvalidCampaignBeforeMutation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runID := startTestRun(t, DefaultCap)
	if _, err := OpenWave(runID); err != nil {
		t.Fatalf("OpenWave: %v", err)
	}
	if _, err := NextPass(runID, "wave"); err != nil {
		t.Fatalf("NextPass: %v", err)
	}
	if err := os.WriteFile(paths.ForgeCampaign(runID), []byte(`{"schemaVersion":1,"axes":[]}`), 0o600); err != nil {
		t.Fatalf("write invalid campaign: %v", err)
	}
	before, err := os.ReadFile(paths.ForgeRunState(runID))
	if err != nil {
		t.Fatalf("read state before: %v", err)
	}
	if _, err := CloseWave(runID, 0); !errors.Is(err, ErrCampaignInvalid) {
		t.Fatalf("CloseWave error = %v; want ErrCampaignInvalid", err)
	}
	after, err := os.ReadFile(paths.ForgeRunState(runID))
	if err != nil {
		t.Fatalf("read state after: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed close changed run state")
	}
}

func TestCampaignDeletionDoesNotDowngradeRun(t *testing.T) {
	validCampaign := `{"schemaVersion":1,"axes":[{"title":"A","scenarios":[{"title":"S","steps":["Do"],"expected":"Done"}]}]}`
	outcome := `{"schemaVersion":1,"outcomes":[{"axis":"A","scenario":"S","status":"finding"}]}`
	for _, test := range []struct {
		name  string
		setup func(t *testing.T, runID string)
		op    func(runID string) error
	}{
		{
			name: "after save",
			setup: func(t *testing.T, runID string) {
				if err := SaveCampaign(runID, writeTestFile(t, "campaign.json", validCampaign)); err != nil {
					t.Fatal(err)
				}
				if _, err := OpenWave(runID); err != nil {
					t.Fatal(err)
				}
			},
			op: func(runID string) error { _, err := NextPass(runID, "wave"); return err },
		},
		{
			name: "after pass",
			setup: func(t *testing.T, runID string) {
				if err := SaveCampaign(runID, writeTestFile(t, "campaign.json", validCampaign)); err != nil {
					t.Fatal(err)
				}
				if _, err := OpenWave(runID); err != nil {
					t.Fatal(err)
				}
				if _, err := NextPass(runID, "wave"); err != nil {
					t.Fatal(err)
				}
			},
			op: func(runID string) error { _, err := CloseWave(runID, 1); return err },
		},
		{
			name: "after outcome",
			setup: func(t *testing.T, runID string) {
				if err := SaveCampaign(runID, writeTestFile(t, "campaign.json", validCampaign)); err != nil {
					t.Fatal(err)
				}
				if _, err := OpenWave(runID); err != nil {
					t.Fatal(err)
				}
				if _, err := NextPass(runID, "wave"); err != nil {
					t.Fatal(err)
				}
				if err := RecordOutcome(runID, "wave-1", writeTestFile(t, "outcome.json", outcome)); err != nil {
					t.Fatal(err)
				}
			},
			op: func(runID string) error { _, err := CloseWave(runID, 1); return err },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			runID := startTestRun(t, 2)
			test.setup(t, runID)
			if err := os.Remove(paths.ForgeCampaign(runID)); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(paths.ForgeRunState(runID))
			if err != nil {
				t.Fatal(err)
			}
			if err := test.op(runID); !errors.Is(err, ErrCampaignInvalid) {
				t.Fatalf("operation error = %v; want ErrCampaignInvalid", err)
			}
			after, err := os.ReadFile(paths.ForgeRunState(runID))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("missing campaign changed run state")
			}
		})
	}
}

func TestCampaignlessPassOperationsReadLedger(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runID := startTestRun(t, 2)
	if err := os.WriteFile(paths.ForgeLedger(runID), []byte(`{"schemaVersion":1,"passes":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NextPass(runID, "review"); err == nil {
		t.Fatal("malformed ledger was ignored")
	}
	if err := os.WriteFile(paths.ForgeLedger(runID), []byte(`{"schemaVersion":1,"passes":[{"passId":"review-1","kind":"review","outcomes":[{"axis":"A","scenario":"S","status":"pass"}],"counts":{"pass":1}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(paths.ForgeRunState(runID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NextPass(runID, "review"); !errors.Is(err, ErrCampaignInvalid) {
		t.Fatalf("populated ledger error = %v; want ErrCampaignInvalid", err)
	}
	after, err := os.ReadFile(paths.ForgeRunState(runID))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("populated ledger changed run state")
	}
	closeRun := startTestRun(t, 1)
	if _, err := OpenWave(closeRun); err != nil {
		t.Fatal(err)
	}
	if _, err := NextPass(closeRun, "wave"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ForgeLedger(closeRun), []byte(`{"schemaVersion":1,"passes":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CloseWave(closeRun, 0); err == nil {
		t.Fatal("CloseWave ignored malformed ledger")
	}
	if err := os.WriteFile(paths.ForgeLedger(closeRun), []byte(`{"schemaVersion":1,"passes":[{"passId":"wave-1","kind":"wave","wave":1,"outcomes":[{"axis":"A","scenario":"S","status":"pass"}],"counts":{"pass":1}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CloseWave(closeRun, 0); !errors.Is(err, ErrCampaignInvalid) {
		t.Fatalf("CloseWave populated ledger error = %v; want ErrCampaignInvalid", err)
	}
}

func TestCloseWaveCampaignErrorPrecedesPassStructure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runID := startTestRun(t, 1)
	if err := os.WriteFile(paths.ForgeCampaign(runID), []byte(`{"schemaVersion":1,"axes":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CloseWave(runID, 0); !errors.Is(err, ErrCampaignInvalid) {
		t.Fatalf("CloseWave error = %v; want ErrCampaignInvalid", err)
	}
}

func TestExistingRunWithCampaignBecomesStrictWhenObserved(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runID := startTestRun(t, 1)
	if err := os.WriteFile(paths.ForgeCampaign(runID), []byte(`{"schemaVersion":1,"axes":[{"title":"A","scenarios":[{"title":"S","steps":["Do"],"expected":"Done"}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenWave(runID); err != nil {
		t.Fatal(err)
	}
	if _, err := NextPass(runID, "wave"); err != nil {
		t.Fatal(err)
	}
	state, err := readRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if !state.CampaignRequired {
		t.Fatal("observed campaign did not persist strict mode")
	}
	if err := os.Remove(paths.ForgeCampaign(runID)); err != nil {
		t.Fatal(err)
	}
	if _, err := CloseWave(runID, 0); !errors.Is(err, ErrCampaignInvalid) {
		t.Fatalf("deleted campaign error = %v; want ErrCampaignInvalid", err)
	}
}

func TestOpenWaveRejectsAfterDryWave(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runID := startTestRun(t, 2)
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
	if err := RecordOutcome(runID, "wave-1", outcome); err != nil {
		t.Fatalf("RecordOutcome: %v", err)
	}
	if decision, err := CloseWave(runID, 0); err != nil || decision != "dry" {
		t.Fatalf("CloseWave = %q, %v; want dry", decision, err)
	}
	if _, err := OpenWave(runID); !errors.Is(err, ErrInvalidPass) {
		t.Fatalf("OpenWave after dry wave error = %v; want ErrInvalidPass", err)
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
	if state.NextReview != 3 || state.NextRepair != 3 {
		t.Fatalf("next counters = review %d, repair %d; want 3, 3", state.NextReview, state.NextRepair)
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

func TestAuxiliaryPassCaps(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runID := startTestRun(t, 1)
	campaign := writeTestFile(t, "campaign.json", `{"schemaVersion":1,"axes":[{"title":"A","scenarios":[{"title":"S","steps":["Do"],"expected":"Done"}]}]}`)
	outcome := writeTestFile(t, "outcome.json", `{"schemaVersion":1,"outcomes":[{"axis":"A","scenario":"S","status":"pass"}]}`)
	if err := SaveCampaign(runID, campaign); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"review", "repair"} {
		for sequence := 1; sequence <= MaxAuxiliaryPasses; sequence++ {
			path, err := NextPass(runID, kind)
			if err != nil {
				t.Fatalf("NextPass %s-%d: %v", kind, sequence, err)
			}
			if filepath.Base(path) != fmt.Sprintf("%s-%d", kind, sequence) {
				t.Fatalf("pass path = %s, want %s-%d", path, kind, sequence)
			}
			if err := RecordOutcome(runID, filepath.Base(path), outcome); err != nil {
				t.Fatalf("RecordOutcome %s: %v", kind, err)
			}
		}
		if _, err := NextPass(runID, kind); !errors.Is(err, ErrInvalidPass) {
			t.Fatalf("NextPass over %s cap = %v; want ErrInvalidPass", kind, err)
		}
	}
	state, err := readRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Passes) != 2*MaxAuxiliaryPasses || len(state.Passes) > MaxPasses {
		t.Fatalf("persisted passes = %d; want %d and <= %d", len(state.Passes), 2*MaxAuxiliaryPasses, MaxPasses)
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

func TestRecordOutcomeRejectsMismatchedStaleOpenPass(t *testing.T) {
	tests := []struct {
		name    string
		outcome string
	}{
		{
			name:    "status",
			outcome: `{"schemaVersion":1,"outcomes":[{"axis":"A","scenario":"S1","status":"finding"},{"axis":"A","scenario":"S2","status":"pass"}]}`,
		},
		{
			name:    "reason",
			outcome: `{"schemaVersion":1,"outcomes":[{"axis":"A","scenario":"S1","status":"pass","reason":"changed"},{"axis":"A","scenario":"S2","status":"pass"}]}`,
		},
		{
			name:    "scenario",
			outcome: `{"schemaVersion":1,"outcomes":[{"axis":"A","scenario":"S3","status":"pass"},{"axis":"A","scenario":"S2","status":"pass"}]}`,
		},
		{
			name:    "ordering",
			outcome: `{"schemaVersion":1,"outcomes":[{"axis":"A","scenario":"S2","status":"pass"},{"axis":"A","scenario":"S1","status":"pass"}]}`,
		},
		{
			name:    "invalid data",
			outcome: `{"schemaVersion":1,"outcomes":[{"axis":"A","scenario":"S1","status":"invalid"},{"axis":"A","scenario":"S2","status":"pass"}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			runID := startTestRun(t, DefaultCap)
			campaign := writeTestFile(t, "campaign.json", `{"schemaVersion":1,"axes":[{"title":"A","scenarios":[{"title":"S1","steps":["Do"],"expected":"Done"},{"title":"S2","steps":["Do"],"expected":"Done"},{"title":"S3","steps":["Do"],"expected":"Done"}]}]}`)
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
						Outcomes: []Outcome{
							{Axis: "A", Scenario: "S1", Status: "pass"},
							{Axis: "A", Scenario: "S2", Status: "pass"},
						},
						Counts: counts{Pass: 2},
					}},
				})
			}); err != nil {
				t.Fatalf("seed ledger: %v", err)
			}

			if err := RecordOutcome(runID, "wave-1", writeTestFile(t, "outcome.json", tt.outcome)); !errors.Is(err, ErrInvalidPass) {
				t.Fatalf("RecordOutcome error = %v, want ErrInvalidPass", err)
			}
			state, err := readRun(runID)
			if err != nil {
				t.Fatalf("readRun: %v", err)
			}
			if state.OpenPass != "wave-1" {
				t.Fatalf("OpenPass = %q, want wave-1", state.OpenPass)
			}
		})
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

	campaignlessRunID := startTestRun(t, DefaultCap)
	if _, err := OpenWave(campaignlessRunID); err != nil {
		t.Fatalf("OpenWave without campaign: %v", err)
	}
	if _, err := NextPass(campaignlessRunID, "wave"); err != nil {
		t.Fatalf("NextPass without campaign: %v", err)
	}
	if err := SaveCampaign(campaignlessRunID, initial); !errors.Is(err, ErrInvalidStaging) {
		t.Fatalf("SaveCampaign after campaign-less pass error = %v", err)
	}
}

func TestGoldenSummaryAndFrenchTestplan(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runID := startTestRun(t, DefaultCap)
	copyFixture(t, "campaign.json", paths.ForgeCampaign(runID))
	copyFixture(t, "ledger.json", paths.ForgeLedger(runID))
	if err := withRunLock(runID, func() error {
		state, err := readRun(runID)
		if err != nil {
			return err
		}
		state.Passes = []pass{
			{ID: "wave-1", Kind: passWave, Wave: 1},
			{ID: "repair-1", Kind: passRepair},
			{ID: "review-1", Kind: passReview},
		}
		return writeJSON(paths.ForgeRunState(runID), state)
	}); err != nil {
		t.Fatalf("seed allocated passes: %v", err)
	}
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
	if err := SetRef(runID, "testplan", "document-42"); err != nil {
		t.Fatalf("SetRef testplan: %v", err)
	}
	if _, _, err := RenderTestplan(runID, "en", ""); err != nil {
		t.Fatalf("RenderTestplan: %v", err)
	}
	outputPath := filepath.Join(t.TempDir(), "testplan.md")
	if _, _, err := RenderTestplan(runID, "en", outputPath); err != nil {
		t.Fatalf("RenderTestplan with output: %v", err)
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
		"forge:outcome-recorded",
		"forge:wave-close",
		"forge:report-linked",
		"forge:testplan-linked",
		"forge:testplan-published",
		"forge:testplan-published",
	}
	wantPayloads := []map[string]any{
		{"runId": runID, "ticket": "VAL-306", "cap": float64(1)},
		{"runId": runID, "axes": float64(1), "scenarios": float64(1)},
		{"runId": runID, "wave": float64(1)},
		{"runId": runID, "passId": "wave-1", "kind": "wave", "wave": float64(1)},
		{"runId": runID, "passId": "wave-1", "kind": "wave", "wave": float64(1), "pass": float64(1), "finding": float64(0), "notExercised": float64(0)},
		{"runId": runID, "wave": float64(1), "findings": float64(0), "decision": "dry"},
		{"runId": runID, "report": "comment-42"},
		{"runId": runID, "testplan": "document-42"},
		{"runId": runID, "language": "en", "path": ""},
		{"runId": runID, "language": "en", "path": "testplan.md"},
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

func TestTestplanOutputKeepsOpenedParentAcrossPathSwap(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := Start("VAL-306", "session-306", 1); err != nil {
		t.Fatalf("Start: %v", err)
	}
	parent := t.TempDir()
	backup := parent + "-opened"
	output := filepath.Join(parent, "testplan.md")
	dir, err := openTestplanOutputDir(output, func() {
		if err := os.Rename(parent, backup); err != nil {
			t.Fatalf("move output parent: %v", err)
		}
		if err := os.Symlink(paths.Forge(), parent); err != nil {
			t.Fatalf("replace output parent: %v", err)
		}
	})
	if err != nil {
		t.Fatalf("openTestplanOutputDir: %v", err)
	}
	defer dir.Close()
	if err := writeTestplanOutput(dir, "testplan.md", []byte("safe\n")); err != nil {
		t.Fatalf("writeTestplanOutput: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(backup, "testplan.md")); err != nil || string(got) != "safe\n" {
		t.Fatalf("opened directory output = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(paths.Forge(), "testplan.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("forge was replaced through swapped parent: %v", err)
	}
}

func TestPendingEventsSurviveOutboxFailureAndFlushInOrder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Dir(paths.Outbox()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Outbox(), []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}

	runID, err := Start("VAL-306", "session-306", 1)
	if err != nil || runID == "" {
		t.Fatalf("Start = %q, %v; want recoverable run ID", runID, err)
	}
	if wave, err := OpenWave(runID); err != nil || wave != 1 {
		t.Fatalf("OpenWave = %d, %v; want successful state transition", wave, err)
	}
	state, err := readRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.PendingEvents) != 2 {
		t.Fatalf("pending events = %d, want 2", len(state.PendingEvents))
	}

	if err := os.Remove(paths.Outbox()); err != nil {
		t.Fatal(err)
	}
	if _, err := Status(runID); err != nil {
		t.Fatalf("Status flush: %v", err)
	}
	files, err := outbox.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("flushed events = %d, want 2", len(files))
	}
	wantTypes := []string{"forge:run-start", "forge:wave-open"}
	for i, file := range files {
		event, err := outbox.Read(file)
		if err != nil {
			t.Fatal(err)
		}
		if event.Type != wantTypes[i] || event.Payload["runId"] != runID {
			t.Fatalf("event %d = %+v, want %s for %s", i, event, wantTypes[i], runID)
		}
	}
	if _, err := Status(runID); err != nil {
		t.Fatalf("second Status: %v", err)
	}
	filesAgain, err := outbox.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(filesAgain) != len(files) {
		t.Fatalf("events after idempotent retry = %d, want %d", len(filesAgain), len(files))
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

func TestConsumersRejectCrossFileLedgerForgery(t *testing.T) {
	consumerNames := []string{"CloseWave", "Summary", "RenderTestplan"}
	for _, scenario := range []struct {
		name   string
		mutate func(*ledger)
	}{
		{
			name: "unknown scenario",
			mutate: func(value *ledger) {
				value.Passes[0].Outcomes[0].Scenario = "forged"
			},
		},
		{
			name: "unallocated pass ID",
			mutate: func(value *ledger) {
				value.Passes[0].PassID = "wave-999"
			},
		},
	} {
		for _, consumer := range consumerNames {
			t.Run(scenario.name+"/"+consumer, func(t *testing.T) {
				t.Setenv("HOME", t.TempDir())
				runID := startTestRun(t, DefaultCap)
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
				if err := withRunLock(runID, func() error {
					value, err := readLedger(runID)
					if err != nil {
						return err
					}
					scenario.mutate(value)
					return writeJSON(paths.ForgeLedger(runID), value)
				}); err != nil {
					t.Fatalf("forge ledger: %v", err)
				}

				var err error
				switch consumer {
				case "CloseWave":
					_, err = CloseWave(runID, 0)
				case "Summary":
					_, err = Summary(runID)
				case "RenderTestplan":
					_, _, err = RenderTestplan(runID, "en", "")
				}
				if err == nil || errors.Is(err, ErrInvalidPass) || errors.Is(err, ErrInvalidStaging) || errors.Is(err, ErrCampaignInvalid) {
					t.Fatalf("%s error = %v; want generic persisted-data error", consumer, err)
				}
			})
		}
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
