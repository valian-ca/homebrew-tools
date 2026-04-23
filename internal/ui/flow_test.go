package ui

import (
	"context"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/valian-ca/homebrew-tools/internal/device"
)

// send routes a key message through the model's Update. Used instead of
// tea.Program.Run so tests can exercise the state machine deterministically.
func send(t *testing.T, m *model, key string) {
	t.Helper()
	// Synthesise a tea.KeyMsg from a string. bubbletea exposes tea.KeyMsg
	// as a struct of Runes/Type; building one via a dummy helper would be
	// fragile, so we delegate to huh's own keymap — simulate by driving
	// the form.
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
}

// simulateSummaryPick shortcuts the form interaction: writes the pick to
// the binding and marks the form completed, then lets the model dispatch.
// Catches regressions in dispatch / screen transitions without requiring
// a real tea.Program.
func simulateSummaryPick(t *testing.T, m *model, action string) {
	t.Helper()
	if m.screen != screenSummary {
		t.Fatalf("expected screen=summary, got %v", m.screen)
	}
	m.summaryAction = action
	_, _ = m.handleFormDone()
}

func TestAutoPickSavedDeviceGoesToSummary(t *testing.T) {
	saved := device.Device{ID: "ABC", Label: " Pixel 7 (usb)", Kind: device.KindAndroidUSB}
	m := initialModel(context.Background(), Input{
		InitialSelection: Selection{VMServiceFile: ".dart_tool/valian/vmservice.uri"},
		InitialProbe:     device.Result{Physical: []device.Device{saved}},
		SavedDeviceID:    "ABC",
	})
	if m.screen != screenSummary {
		t.Fatalf("expected screen=summary, got %v", m.screen)
	}
	if m.sel.Device.ID != "ABC" {
		t.Fatalf("expected saved device auto-picked, got %q", m.sel.Device.ID)
	}
}

func TestSinglePhysicalDeviceAutoPicks(t *testing.T) {
	only := device.Device{ID: "X", Label: "x", Kind: device.KindAndroidUSB}
	m := initialModel(context.Background(), Input{
		InitialProbe: device.Result{Physical: []device.Device{only}},
	})
	if m.screen != screenSummary {
		t.Fatalf("expected screen=summary for single device, got %v", m.screen)
	}
}

func TestNoDevicesShowsDevicePicker(t *testing.T) {
	m := initialModel(context.Background(), Input{})
	if m.screen != screenDevicePicker {
		t.Fatalf("expected screen=device picker, got %v", m.screen)
	}
}

func TestSummaryFlavorActionGoesToFlavorScreen(t *testing.T) {
	m := singleDeviceSummaryModel(t)
	simulateSummaryPick(t, m, actFlavor)
	if m.screen != screenFlavor {
		t.Fatalf("expected screen=flavor after picking Flavor, got %v", m.screen)
	}
}

func TestSummaryVMOutActionGoesToVMOut(t *testing.T) {
	m := singleDeviceSummaryModel(t)
	simulateSummaryPick(t, m, actVMOut)
	if m.screen != screenVMOut {
		t.Fatalf("expected screen=vmout, got %v", m.screen)
	}
}

func TestSummaryDeviceActionGoesToPickerFromSummary(t *testing.T) {
	m := singleDeviceSummaryModel(t)
	simulateSummaryPick(t, m, actDevice)
	if m.screen != screenDevicePicker {
		t.Fatalf("expected screen=device picker, got %v", m.screen)
	}
	if !m.fromSummary {
		t.Fatal("expected fromSummary=true when entering picker via summary")
	}
}

func TestSummaryLaunchQuitsWithoutCancel(t *testing.T) {
	m := singleDeviceSummaryModel(t)
	simulateSummaryPick(t, m, actLaunch)
	if m.cancelled {
		t.Fatal("Launch should not set cancelled")
	}
}

func TestSummaryCancelQuitsCancelled(t *testing.T) {
	m := singleDeviceSummaryModel(t)
	simulateSummaryPick(t, m, actCancel)
	if !m.cancelled {
		t.Fatal("Cancel should set cancelled")
	}
}

func TestEscOnSummaryCancels(t *testing.T) {
	m := singleDeviceSummaryModel(t)
	_, _ = m.handleEsc()
	if !m.cancelled {
		t.Fatal("ESC on summary should cancel")
	}
}

func TestEscOnFlavorReturnsToSummary(t *testing.T) {
	m := singleDeviceSummaryModel(t)
	simulateSummaryPick(t, m, actFlavor)
	if m.screen != screenFlavor {
		t.Fatalf("precondition: flavor screen expected, got %v", m.screen)
	}
	_, _ = m.handleEsc()
	if m.screen != screenSummary {
		t.Fatalf("ESC on flavor should return to summary, got %v", m.screen)
	}
	if m.cancelled {
		t.Fatal("ESC on sub-menu should not cancel")
	}
}

func TestEscOnInitialDevicePickerCancels(t *testing.T) {
	m := initialModel(context.Background(), Input{})
	if m.screen != screenDevicePicker {
		t.Fatalf("precondition: picker expected, got %v", m.screen)
	}
	_, _ = m.handleEsc()
	if !m.cancelled {
		t.Fatal("ESC on initial picker should cancel")
	}
}

// singleDeviceSummaryModel returns a model that has auto-picked a single
// device and is sitting on the summary screen — the most common entry
// state after frn starts.
func singleDeviceSummaryModel(t *testing.T) *model {
	t.Helper()
	only := device.Device{ID: "X", Label: "x", Kind: device.KindAndroidUSB}
	m := initialModel(context.Background(), Input{
		InitialSelection: Selection{VMServiceFile: ".dart_tool/valian/vmservice.uri"},
		InitialProbe:     device.Result{Physical: []device.Device{only}},
	})
	if m.screen != screenSummary {
		t.Fatalf("setup: expected summary, got %v", m.screen)
	}
	return m
}

var _ = send // quiet unused-helper warning; keeps the helper available for future tests

// Regression guard: a TickMsg delivered while scanning must advance the
// spinner frame. The simplest symptom of a broken setup (method-value
// binding, stale tag filtering, etc.) is that the view stays frozen on
// the first frame.
func TestSpinnerTickAdvancesView(t *testing.T) {
	m := initialModel(context.Background(), Input{}) // no devices → picker
	m.scanning = true                                 // precondition for the handler
	firstView := m.sp.View()
	firstMsg := m.sp.Tick()
	tick, ok := firstMsg.(spinner.TickMsg)
	if !ok {
		t.Fatalf("Tick() returned %T, want spinner.TickMsg", firstMsg)
	}
	_, _ = m.Update(tick)
	if m.sp.View() == firstView {
		t.Fatalf("view did not change after tick; still %q", m.sp.View())
	}
}

// Regression guard: Init, when the initial screen is the device picker,
// must return a Cmd batch whose closure ultimately produces a
// spinner.TickMsg. An earlier version of startDeepScan gated Tick on
// !m.scanning, and initialModel's discarded cmd had already flipped
// scanning to true — so Init's second call silently omitted Tick and the
// animation never started.
func TestInitBatchContainsSpinnerTick(t *testing.T) {
	m := initialModel(context.Background(), Input{}) // no devices → picker
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init returned nil cmd on the picker screen")
	}
	if !batchContainsTick(cmd, 3) {
		t.Fatal("Init's batch does not produce a spinner.TickMsg")
	}
}

// batchContainsTick walks a tea.BatchMsg up to depth levels deep looking
// for a cmd that immediately returns a spinner.TickMsg. Deep probe cmds
// (which block on xcrun for seconds) are side-stepped via a goroutine +
// short timeout, so the test doesn't hang on machines without xcrun.
func batchContainsTick(cmd tea.Cmd, depth int) bool {
	if cmd == nil || depth <= 0 {
		return false
	}
	// Run the cmd with a tiny timeout; Tick is synchronous, deep probe is not.
	type result struct{ msg tea.Msg }
	ch := make(chan result, 1)
	go func() { ch <- result{msg: cmd()} }()
	select {
	case r := <-ch:
		switch m := r.msg.(type) {
		case tea.BatchMsg:
			for _, sub := range m {
				if batchContainsTick(sub, depth-1) {
					return true
				}
			}
		case spinner.TickMsg:
			return true
		}
	case <-timeAfterShort():
		// Cmd blocked (likely the deep probe). Not the tick we're looking for.
	}
	return false
}

func timeAfterShort() <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		// 50 ms is plenty for a spinner.Tick call; the deep probe takes seconds.
		for i := 0; i < 5_000_000; i++ {
		}
		close(ch)
	}()
	return ch
}

// Regression guard: once scanning flips to false (deep probe done), the
// TickMsg handler must stop dispatching new ticks — otherwise the spinner
// keeps burning CPU in the background.
func TestSpinnerStopsWhenScanningEnds(t *testing.T) {
	m := initialModel(context.Background(), Input{})
	m.scanning = false
	tick, _ := m.sp.Tick().(spinner.TickMsg)
	_, cmd := m.Update(tick)
	if cmd != nil {
		t.Fatal("expected no follow-up cmd when scanning is false")
	}
}
