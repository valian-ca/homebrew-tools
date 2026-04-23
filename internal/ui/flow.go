// Package ui owns the interactive menu flow. It runs as a single
// bubbletea program so transitions between screens redraw in place
// without flicker, without clearing the terminal, and without leaving
// stale output behind.
package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"github.com/valian-ca/homebrew-tools/internal/adb"
	"github.com/valian-ca/homebrew-tools/internal/device"
)

// ErrCancelled indicates the user aborted the flow.
var ErrCancelled = errors.New("cancelled")

// Selection holds the running choices as the user navigates the menus.
type Selection struct {
	Flavor        string
	HasFlavor     bool // true once the user has explicitly set a flavor.
	Device        device.Device
	VMServiceFile string
}

// FlutterArgs returns the argv (after "flutter run") that will be passed
// through. Shared between the summary preview and the actual exec so
// what the user sees is exactly what runs.
func (s Selection) FlutterArgs(extra []string) []string {
	var args []string
	if s.Flavor != "" {
		args = append(args, "--flavor", s.Flavor)
	}
	args = append(args, "--vmservice-out-file="+s.VMServiceFile, "-d", s.Device.ID)
	args = append(args, extra...)
	return args
}

// Input configures a Run invocation.
type Input struct {
	InitialSelection Selection
	InitialProbe     device.Result
	SavedDeviceID    string
	AvailableFlavors []string
	// ExtraArgs are the pass-through args appended after the device flag.
	// Used in the summary preview so the shown command matches what runs.
	ExtraArgs []string
}

// Output is returned when the user confirms with Launch.
type Output struct {
	Selection Selection
	Probe     device.Result
}

// Nerd Font glyphs. Kept as literal UTF-8 so the bytes round-trip through
// `go fmt` unchanged; see CLAUDE.md for the byte sequences to verify.
const (
	glyphRescan    = ""
	glyphRestart   = ""
	glyphReconnect = ""
)

// Sentinel option values returned by each form. Opaque strings — the model
// dispatches on them in handleFormDone.
const (
	// device picker
	optRescan    = "__rescan__"
	optRestart   = "__restart__"
	optReconnect = "__reconnect__"
	optSepEmus   = "__sep_emus__"
	optSepActs   = "__sep_acts__"
	optCancel    = "__cancel__"

	// summary
	actLaunch = "launch"
	actFlavor = "flavor"
	actDevice = "device"
	actVMOut  = "vmout"
	actCancel = "cancel"

	// flavor sub-menu
	valNone   = "__none__"
	valCustom = "__custom__"
)

type screen int

const (
	screenDevicePicker screen = iota
	screenSummary
	screenFlavor
	screenFlavorCustom
	screenVMOut
	screenBusy
)

// Run runs the full menu flow. Returns ErrCancelled when the user aborts.
func Run(ctx context.Context, in Input) (Output, error) {
	m := initialModel(ctx, in)
	final, err := tea.NewProgram(m).Run()
	if err != nil {
		return Output{}, err
	}
	fm, ok := final.(*model)
	if !ok || fm == nil {
		return Output{}, fmt.Errorf("unexpected final model type %T", final)
	}
	if fm.cancelled {
		return Output{Selection: fm.sel, Probe: fm.probe}, ErrCancelled
	}
	return Output{Selection: fm.sel, Probe: fm.probe}, nil
}

type model struct {
	ctx       context.Context
	flavors   []string
	extraArgs []string
	sel       Selection
	probe     device.Result

	screen      screen
	form        *huh.Form
	fromSummary bool // true when we entered the device picker from summary

	// bound values for active forms — reset per transition
	deviceChoice  string
	summaryAction string
	flavorChoice  string
	flavorCustom  string
	vmOutValue    string

	// transient
	status string // one-line message shown above busy screens

	// deep iOS scan (xcdevice) runs in parallel with the fast probes.
	// scanning stays true between launch and result; the spinner ticks
	// only while it's true.
	sp       spinner.Model
	scanning bool

	// outcome
	cancelled bool
}

func initialModel(ctx context.Context, in Input) *model {
	sp := spinner.New()
	// Meter animates across ASCII blocks — visible with any font, no Nerd
	// Font fallback issues, and unambiguously distinct between frames.
	sp.Spinner = spinner.Meter
	m := &model{
		ctx:       ctx,
		sel:       in.InitialSelection,
		probe:     in.InitialProbe,
		flavors:   in.AvailableFlavors,
		extraArgs: in.ExtraArgs,
		sp:        sp,
	}
	// Auto-pick: saved device still connected, or exactly one device.
	if in.SavedDeviceID != "" {
		if d, ok := in.InitialProbe.FindByID(in.SavedDeviceID); ok {
			m.sel.Device = d
			m.enterSummary()
			return m
		}
	}
	all := in.InitialProbe.All()
	if len(all) == 1 {
		m.sel.Device = all[0]
		m.enterSummary()
		return m
	}
	m.enterDevicePicker(false)
	return m
}

// keymap: we handle ctrl+c and esc globally at the model level, so huh
// should not bind them to anything on its own.
func frnKeyMap() *huh.KeyMap {
	km := huh.NewDefaultKeyMap()
	km.Quit = key.NewBinding(key.WithKeys("ctrl+alt+q")) // effectively disabled
	return km
}

func newForm(fields ...huh.Field) *huh.Form {
	f := huh.NewForm(huh.NewGroup(fields...))
	f.WithKeyMap(frnKeyMap()).WithShowHelp(false).WithShowErrors(false)
	return f
}

func (m *model) enterDevicePicker(fromSummary bool) tea.Cmd {
	m.screen = screenDevicePicker
	m.fromSummary = fromSummary
	m.deviceChoice = ""
	m.form = buildDevicePickerForm(m.probe, &m.deviceChoice)
	cmds := []tea.Cmd{m.form.Init()}
	// Kick off (or re-kick) the slow iOS scan. The fast probe has already
	// populated m.probe; xcdevice can take ~2s but may reveal devices that
	// devicectl's cache misses.
	cmds = append(cmds, m.startDeepScan())
	return tea.Batch(cmds...)
}

// startDeepScan fires the xcdevice probe and a fresh spinner tick.
// Always appending Tick is safe: spinner.Model.Update uses a monotonic
// "tag" to drop ticks from stale loops, so short-lived concurrent loops
// (e.g. user rescans mid-flight) resolve themselves.
//
// This is intentionally side-effect-only on m.scanning rather than also
// gating Tick on it — earlier drafts did the latter and the gate silently
// broke the animation when initialModel discarded the first enterDevicePicker
// Cmd (the side effect fired but the bound Tick never ran, and Init's
// subsequent call saw scanning=true and skipped Tick).
func (m *model) startDeepScan() tea.Cmd {
	m.scanning = true
	return tea.Batch(deepProbeCmd(m.ctx), m.sp.Tick)
}

func (m *model) enterSummary() tea.Cmd {
	m.screen = screenSummary
	m.summaryAction = ""
	m.form = buildSummaryForm(m.sel, m.extraArgs, &m.summaryAction)
	return m.form.Init()
}

func (m *model) enterFlavor() tea.Cmd {
	m.screen = screenFlavor
	if m.sel.Flavor == "" {
		m.flavorChoice = valNone
	} else {
		m.flavorChoice = m.sel.Flavor
	}
	m.form = buildFlavorForm(m.flavors, m.sel.Flavor, &m.flavorChoice)
	return m.form.Init()
}

func (m *model) enterFlavorCustom() tea.Cmd {
	m.screen = screenFlavorCustom
	m.flavorCustom = m.sel.Flavor
	m.form = newForm(
		huh.NewInput().Title("Flavor").Prompt("> ").Value(&m.flavorCustom),
	)
	return m.form.Init()
}

func (m *model) enterVMOut() tea.Cmd {
	m.screen = screenVMOut
	m.vmOutValue = m.sel.VMServiceFile
	m.form = newForm(
		huh.NewInput().Title("VM service file").Prompt("> ").Value(&m.vmOutValue),
	)
	return m.form.Init()
}

func (m *model) Init() tea.Cmd {
	if m.form == nil {
		return nil
	}
	cmds := []tea.Cmd{m.form.Init()}
	// On program start, if we land on the picker (no auto-pick), begin the
	// slow iOS scan immediately so its result arrives ~2s later.
	if m.screen == screenDevicePicker {
		cmds = append(cmds, m.startDeepScan())
	}
	return tea.Batch(cmds...)
}

// tea.Msg kinds we send between the model and our commands.
type (
	probeDoneMsg     struct{ result device.Result }
	deepProbeDoneMsg struct{ devices []device.Device }
	actionLog        string // line to show in the status area briefly
)

func probeCmd(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		return probeDoneMsg{result: device.ProbeAll(ctx)}
	}
}

// deepProbeCmd runs xcdevice list in the background. Its result is merged
// into m.probe when it arrives; until then the spinner indicates activity.
func deepProbeCmd(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		return deepProbeDoneMsg{devices: device.DeepProbeIOS(ctx)}
	}
}

func restartAdbCmd(ctx context.Context) tea.Cmd {
	return tea.Sequence(
		func() tea.Msg { return actionLog("Restarting adb...") },
		func() tea.Msg {
			// Write to a buffer so the UI stays clean; output is discarded.
			var buf bytes.Buffer
			_ = adb.Restart(ctx, &buf)
			return probeDoneMsg{result: device.ProbeAll(ctx)}
		},
	)
}

func reconnectWirelessCmd(ctx context.Context) tea.Cmd {
	return tea.Sequence(
		func() tea.Msg { return actionLog("Reconnecting wireless adb...") },
		func() tea.Msg {
			var buf bytes.Buffer
			_, _ = adb.ReconnectWireless(ctx, &buf)
			return probeDoneMsg{result: device.ProbeAll(ctx)}
		},
	)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			m.cancelled = true
			return m, tea.Quit
		case "esc":
			return m.handleEsc()
		}
	case probeDoneMsg:
		m.probe = msg.result
		m.status = ""
		cmd := m.enterDevicePicker(m.fromSummary)
		return m, cmd
	case deepProbeDoneMsg:
		hadNone := len(m.probe.All()) == 0
		m.probe = device.MergeIOS(m.probe, msg.devices)
		m.scanning = false
		if m.screen == screenDevicePicker {
			all := m.probe.All()
			// If the picker was sitting on "No devices" and the deep scan
			// produced exactly one, skip the picker the same way initial
			// auto-pick does when a single device is present on startup.
			if hadNone && len(all) == 1 {
				m.sel.Device = all[0]
				cmd := m.enterSummary()
				return m, cmd
			}
			m.form = buildDevicePickerForm(m.probe, &m.deviceChoice)
			return m, m.form.Init()
		}
		return m, nil
	case spinner.TickMsg:
		if !m.scanning {
			// Stale tick — previous scan already completed, don't re-dispatch.
			return m, nil
		}
		sp, cmd := m.sp.Update(msg)
		m.sp = sp
		return m, cmd
	case actionLog:
		m.status = string(msg)
		return m, nil
	}

	if m.form == nil {
		return m, nil
	}
	f, cmd := m.form.Update(msg)
	if ff, ok := f.(*huh.Form); ok {
		m.form = ff
	}
	if m.form.State == huh.StateCompleted {
		return m.handleFormDone()
	}
	if m.form.State == huh.StateAborted {
		// Treat huh's own abort (shouldn't happen given our keymap) as ESC.
		return m.handleEsc()
	}
	return m, cmd
}

func (m *model) handleEsc() (tea.Model, tea.Cmd) {
	switch m.screen {
	case screenDevicePicker:
		if m.fromSummary {
			cmd := m.enterSummary()
			return m, cmd
		}
		m.cancelled = true
		return m, tea.Quit
	case screenSummary:
		m.cancelled = true
		return m, tea.Quit
	case screenFlavor, screenFlavorCustom, screenVMOut:
		cmd := m.enterSummary()
		return m, cmd
	case screenBusy:
		// Busy screens can't be cancelled mid-flight — ignore esc.
		return m, nil
	}
	return m, nil
}

func (m *model) handleFormDone() (tea.Model, tea.Cmd) {
	switch m.screen {
	case screenDevicePicker:
		return m.handleDevicePicked()
	case screenSummary:
		return m.handleSummaryAction()
	case screenFlavor:
		return m.handleFlavorPicked()
	case screenFlavorCustom:
		if m.flavorCustom != "" {
			m.sel.Flavor = m.flavorCustom
			m.sel.HasFlavor = true
		}
		cmd := m.enterSummary()
		return m, cmd
	case screenVMOut:
		if m.vmOutValue != "" {
			m.sel.VMServiceFile = m.vmOutValue
		}
		cmd := m.enterSummary()
		return m, cmd
	}
	return m, nil
}

func (m *model) handleDevicePicked() (tea.Model, tea.Cmd) {
	switch m.deviceChoice {
	case optCancel:
		m.cancelled = true
		return m, tea.Quit
	case optSepEmus, optSepActs:
		// Re-show the same picker.
		cmd := m.enterDevicePicker(m.fromSummary)
		return m, cmd
	case optRescan:
		m.screen = screenBusy
		m.status = "Probing devices..."
		return m, probeCmd(m.ctx)
	case optRestart:
		m.screen = screenBusy
		return m, restartAdbCmd(m.ctx)
	case optReconnect:
		m.screen = screenBusy
		return m, reconnectWirelessCmd(m.ctx)
	default:
		if d, ok := m.probe.FindByID(m.deviceChoice); ok {
			m.sel.Device = d
			// Auto-advance to summary whether we came in fresh or from it.
			cmd := m.enterSummary()
			return m, cmd
		}
		// Unknown — stay.
		cmd := m.enterDevicePicker(m.fromSummary)
		return m, cmd
	}
}

func (m *model) handleSummaryAction() (tea.Model, tea.Cmd) {
	switch m.summaryAction {
	case actLaunch:
		return m, tea.Quit
	case actCancel:
		m.cancelled = true
		return m, tea.Quit
	case actFlavor:
		cmd := m.enterFlavor()
		return m, cmd
	case actDevice:
		cmd := m.enterDevicePicker(true)
		return m, cmd
	case actVMOut:
		cmd := m.enterVMOut()
		return m, cmd
	}
	return m, nil
}

func (m *model) handleFlavorPicked() (tea.Model, tea.Cmd) {
	switch m.flavorChoice {
	case "":
		// Sub-form cleared without selection — back to summary.
	case valNone:
		m.sel.Flavor = ""
		m.sel.HasFlavor = true
	case valCustom:
		cmd := m.enterFlavorCustom()
		return m, cmd
	default:
		m.sel.Flavor = m.flavorChoice
		m.sel.HasFlavor = true
	}
	cmd := m.enterSummary()
	return m, cmd
}

func (m *model) View() string {
	var b strings.Builder
	if m.status != "" {
		b.WriteString(m.status)
		b.WriteString("\n")
	}
	if m.screen == screenBusy {
		return b.String()
	}
	if m.scanning && m.screen == screenDevicePicker {
		b.WriteString(m.sp.View())
		b.WriteString(" Scanning iOS devices...\n")
	}
	if m.form != nil {
		b.WriteString(m.form.View())
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Form builders
// ---------------------------------------------------------------------------

func buildDevicePickerForm(result device.Result, binding *string) *huh.Form {
	options := make([]huh.Option[string], 0, len(result.Physical)+len(result.Virtual)+5)
	hasPhys := len(result.Physical) > 0
	hasVirt := len(result.Virtual) > 0
	for _, d := range result.Physical {
		options = append(options, huh.NewOption(d.Label, d.ID))
	}
	if hasVirt {
		if hasPhys {
			options = append(options, huh.NewOption("──────── emulators ────────", optSepEmus))
		}
		for _, d := range result.Virtual {
			options = append(options, huh.NewOption(d.Label, d.ID))
		}
	}
	if hasPhys || hasVirt {
		options = append(options, huh.NewOption("──────── actions ────────", optSepActs))
	}
	options = append(options, huh.NewOption(glyphRescan+" Rescan devices", optRescan))
	if _, err := exec.LookPath("adb"); err == nil {
		options = append(options, huh.NewOption(glyphRestart+" Restart adb server", optRestart))
		if !result.AdbTimeout {
			options = append(options, huh.NewOption(glyphReconnect+" Reconnect wireless adb", optReconnect))
		}
	}
	header := "Device"
	if !hasPhys && !hasVirt {
		header = "No devices"
		options = append(options, huh.NewOption("Cancel", optCancel))
		fmt.Fprintln(os.Stderr, "No devices found. Connect a device or start a simulator/emulator, then rescan.")
	}
	return newForm(
		huh.NewSelect[string]().Title(header).Options(options...).Value(binding),
	)
}

func buildSummaryForm(sel Selection, extra []string, binding *string) *huh.Form {
	flavorDisplay := sel.Flavor
	if flavorDisplay == "" {
		flavorDisplay = "(none)"
	}
	preview := "$ flutter run " + strings.Join(sel.FlutterArgs(extra), " ")
	return newForm(
		huh.NewSelect[string]().
			Title("Ready to launch").
			Description(preview).
			Options(
				huh.NewOption("▶ Launch", actLaunch),
				huh.NewOption("Flavor     → "+flavorDisplay, actFlavor),
				huh.NewOption("Device     → "+sel.Device.Label, actDevice),
				huh.NewOption("VM out     → "+sel.VMServiceFile, actVMOut),
				huh.NewOption("Cancel", actCancel),
			).
			Value(binding),
	)
}

func buildFlavorForm(available []string, current string, binding *string) *huh.Form {
	options := make([]huh.Option[string], 0, len(available)+2)
	for _, f := range available {
		options = append(options, huh.NewOption(f, f))
	}
	// When the project advertises flavors, the detected set is
	// authoritative — "(none)" and "Custom…" are almost always typos or
	// footguns. Users with a legitimate need can still force either via
	// `frn --no-flavor` or `frn --flavor=<name>` on the CLI.
	if len(available) == 0 {
		options = append(options,
			huh.NewOption("(none)", valNone),
			huh.NewOption("Custom…", valCustom),
		)
	}
	return newForm(
		huh.NewSelect[string]().Title("Flavor").Options(options...).Value(binding),
	)
}
