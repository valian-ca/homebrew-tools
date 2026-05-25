package tui

import (
	"fmt"
	"os"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/valian-ca/homebrew-tools/internal/review-triage/contract"
)

const splitMinWidth = 160

type Outcome int

const (
	OutcomeNone Outcome = iota
	OutcomeSubmit
	OutcomeCancel
)

type Result struct {
	Outcome   Outcome
	Decisions []contract.Decision
}

type screen int

const (
	screenTable screen = iota
	screenDetail
	screenDiscuss
	screenConfirm
)

type model struct {
	findings []contract.Finding
	actions  []*contract.Action // parallel to findings; nil = undecided
	prompts  []string           // parallel to findings; discuss prompt text

	group  groupBy
	rows   []row
	cursor int

	screen        screen
	width, height int
	noColor       bool
	th            theme

	help     bool
	quitting bool
	footer   string

	vp            viewport.Model
	detailFocused bool // wide split only: the Detail pane holds focus

	ta            textarea.Model
	discussIdx    int
	discussReturn screen
	discussFresh  bool
	discussPrev   *contract.Action

	outcome   Outcome
	decisions []contract.Decision
}

func Run(in *contract.Input) (Result, error) {
	if len(in.Findings) == 0 {
		return Result{Outcome: OutcomeSubmit, Decisions: []contract.Decision{}}, nil
	}
	m := newModel(in, os.Getenv("NO_COLOR") != "")
	final, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if err != nil {
		return Result{}, err
	}
	fm, ok := final.(*model)
	if !ok {
		return Result{}, fmt.Errorf("unexpected final model type %T", final)
	}
	return Result{Outcome: fm.outcome, Decisions: fm.decisions}, nil
}

func newModel(in *contract.Input, noColor bool) *model {
	n := len(in.Findings)
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	m := &model{
		findings: in.Findings,
		actions:  make([]*contract.Action, n),
		prompts:  make([]string, n),
		group:    groupByType,
		noColor:  noColor,
		th:       newTheme(noColor),
		ta:       ta,
		vp:       viewport.New(0, 0),
	}
	m.rebuildRows()
	m.cursor = m.firstItemRow()
	return m
}

func (m *model) Init() tea.Cmd { return nil }

func (m *model) split() bool { return m.width >= splitMinWidth }

// layout returns the content widths (inside the border) of the Table and
// Detail panes. In split mode the two panes plus a one-column gutter tile the
// terminal exactly; in single-pane mode both report the full inner width.
func (m *model) layout() (tableW, detailW int) {
	if !m.split() {
		w := m.width - 2
		if w < 8 {
			w = 8
		}
		return w, w
	}
	leftOuter := m.width * 40 / 100
	tableW = leftOuter - 2
	detailW = m.width - leftOuter - 1 - 2 // 1-col gutter, 2 for the right border
	if tableW < 8 {
		tableW = 8
	}
	if detailW < 8 {
		detailW = 8
	}
	return tableW, detailW
}

func (m *model) rebuildRows() {
	m.rows = buildRows(m.group, m.findings, m.actions)
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
}

func (m *model) firstItemRow() int {
	for i, r := range m.rows {
		if r.kind == rowItem {
			return i
		}
	}
	return 0
}

func (m *model) currentFindingIdx() int {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return -1
	}
	if m.rows[m.cursor].kind != rowItem {
		return -1
	}
	return m.rows[m.cursor].findingIdx
}

func (m *model) groupItemIndices(rowIdx int) []int {
	var out []int
	for i := rowIdx + 1; i < len(m.rows) && m.rows[i].kind == rowItem; i++ {
		out = append(out, m.rows[i].findingIdx)
	}
	return out
}

func (m *model) moveCursor(delta int) {
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
	m.refreshDetail()
}

// advanceToNextItem skips group headers so item actions flow item-to-item
// across group boundaries, landing on the submit row after the last item.
func (m *model) advanceToNextItem() {
	for i := m.cursor + 1; i < len(m.rows); i++ {
		if m.rows[i].kind == rowItem {
			m.cursor = i
			m.refreshDetail()
			return
		}
	}
	m.cursor = len(m.rows) - 1
	m.refreshDetail()
}

func (m *model) advanceToNextHeader() {
	for i := m.cursor + 1; i < len(m.rows); i++ {
		if m.rows[i].kind == rowHeader || m.rows[i].kind == rowSubmit {
			m.cursor = i
			m.refreshDetail()
			return
		}
	}
	m.cursor = len(m.rows) - 1
	m.refreshDetail()
}

func setAction(dst **contract.Action, a contract.Action) { v := a; *dst = &v }

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.relayout()
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *model) relayout() {
	if !m.split() {
		m.detailFocused = false
	}
	_, detailW := m.layout()
	bodyH := m.height - 6
	if bodyH < 3 {
		bodyH = 3
	}
	// Re-render the detail only when the width actually changed: a drag-resize
	// emits a WindowSizeMsg per cell step and re-diffing on each is wasteful.
	widthChanged := detailW != m.vp.Width
	m.vp.Width = detailW
	m.vp.Height = bodyH
	// The Discuss textarea is the input of a small centred modal, not a
	// full-screen field — a bounded size keeps the popup compact.
	taW := m.width - 12
	if taW > 64 {
		taW = 64
	}
	if taW < 16 {
		taW = 16
	}
	m.ta.SetWidth(taW)
	m.ta.SetHeight(6)
	if widthChanged {
		m.refreshDetail()
	}
}

func (m *model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.quitting {
		switch msg.String() {
		case "y", "Y":
			m.outcome = OutcomeCancel
			return m, tea.Quit
		default:
			m.quitting = false
			return m, nil
		}
	}
	if m.help {
		if k := msg.String(); k == "?" || k == "esc" {
			m.help = false
		}
		return m, nil
	}
	// Ctrl+C opens the quit confirm from any screen, including Discuss (where
	// 'n' returns to the textarea with its in-progress note intact).
	if msg.String() == "ctrl+c" {
		m.quitting = true
		return m, nil
	}
	if m.screen == screenDiscuss {
		return m.updateDiscuss(msg)
	}

	switch msg.String() {
	case "?":
		m.help = true
		return m, nil
	case "ctrl+s", "cmd+s":
		return m.attemptSubmit()
	}

	switch m.screen {
	case screenTable:
		return m.updateTable(msg)
	case screenDetail:
		return m.updateDetail(msg)
	case screenConfirm:
		return m.updateConfirm(msg)
	}
	return m, nil
}

func (m *model) attemptSubmit() (tea.Model, tea.Cmd) {
	undecided := 0
	for _, a := range m.actions {
		if a == nil {
			undecided++
		}
	}
	if undecided > 0 {
		m.footer = fmt.Sprintf("%d finding(s) still undecided — decide all before submitting", undecided)
		return m, nil
	}
	m.footer = ""
	m.screen = screenConfirm
	return m, nil
}

func (m *model) finalize() {
	decisions := make([]contract.Decision, len(m.findings))
	for i, f := range m.findings {
		d := contract.Decision{ID: f.ID, Action: *m.actions[i]}
		if *m.actions[i] == contract.ActionDiscuss {
			p := m.prompts[i]
			d.DiscussPrompt = &p
		}
		decisions[i] = d
	}
	m.decisions = decisions
	m.outcome = OutcomeSubmit
}
