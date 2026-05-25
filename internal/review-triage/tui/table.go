package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/valian-ca/homebrew-tools/internal/review-triage/contract"
)

func (m *model) updateTable(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.footer = ""

	// Split mode with the Detail pane focused: navigation scrolls the pane and
	// esc returns focus. Action keys still act on the Table cursor (the Table
	// is the authoritative locus of decisions), so they fall through.
	if m.split() && m.detailFocused {
		switch msg.String() {
		case "esc":
			m.detailFocused = false
			return m, nil
		case "j", "down", "k", "up", "pgup", "pgdown", "ctrl+u", "ctrl+d":
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			return m, cmd
		case "g":
			m.vp.GotoTop()
			return m, nil
		case "G":
			m.vp.GotoBottom()
			return m, nil
		}
	}

	switch msg.String() {
	case "j", "down":
		m.moveCursor(1)
	case "k", "up":
		m.moveCursor(-1)
	case "q", "esc":
		m.quitting = true
	case "g":
		m.cycleGroup()
	case "f":
		m.applyAction(contract.ActionFix)
	case "s":
		m.applyAction(contract.ActionSkip)
	case "d":
		return m.applyDiscuss()
	case "tab":
		m.applyTab()
	case "enter":
		return m.tableEnter()
	}
	return m, nil
}

func (m *model) applyAction(a contract.Action) {
	switch m.rows[m.cursor].kind {
	case rowItem:
		setAction(&m.actions[m.rows[m.cursor].findingIdx], a)
		m.advanceToNextItem()
	case rowHeader:
		for _, idx := range m.groupItemIndices(m.cursor) {
			setAction(&m.actions[idx], a)
		}
		m.advanceToNextHeader()
	}
}

func (m *model) applyDiscuss() (tea.Model, tea.Cmd) {
	switch m.rows[m.cursor].kind {
	case rowItem:
		return m.openDiscuss(m.rows[m.cursor].findingIdx, screenTable)
	case rowHeader:
		for _, idx := range m.groupItemIndices(m.cursor) {
			setAction(&m.actions[idx], contract.ActionDiscuss)
			m.prompts[idx] = ""
		}
		m.advanceToNextHeader()
	}
	return m, nil
}

func (m *model) applyTab() {
	switch m.rows[m.cursor].kind {
	case rowItem:
		idx := m.rows[m.cursor].findingIdx
		sel := m.findings[idx].Selection
		if sel == nil {
			m.footer = "no suggestion to accept on this finding"
			return
		}
		setAction(&m.actions[idx], *sel)
		m.advanceToNextItem()
	case rowHeader:
		for _, idx := range m.groupItemIndices(m.cursor) {
			if sel := m.findings[idx].Selection; sel != nil {
				setAction(&m.actions[idx], *sel)
			}
		}
		m.advanceToNextHeader()
	}
}

func (m *model) tableEnter() (tea.Model, tea.Cmd) {
	switch m.rows[m.cursor].kind {
	case rowItem:
		if m.split() {
			m.detailFocused = true
		} else {
			m.screen = screenDetail
			m.refreshDetail()
		}
	case rowSubmit:
		return m.attemptSubmit()
	}
	return m, nil
}

func (m *model) cycleGroup() {
	m.group = (m.group + 1) % groupByCount
	m.rebuildRows()
	m.cursor = m.firstItemRow()
	m.refreshDetail()
}

func clip(line string, w int) string {
	return ansi.Truncate(line, w, "…")
}

func (m *model) tableInner(w int) string {
	var lines []string
	title := fmt.Sprintf(" Findings · group: %s ", m.group.label())
	lines = append(lines, m.th.titleBar.Width(w).Render(ansi.Truncate(title, w, "")))

	tableFocused := !m.split() || !m.detailFocused
	for i, r := range m.rows {
		mark := "  "
		selected := i == m.cursor && tableFocused
		if selected {
			mark = m.th.cursor.Render("▶ ")
		}
		switch r.kind {
		case rowHeader:
			idxs := m.groupItemIndices(i)
			text := fmt.Sprintf("▼ %s (%d)", r.groupKey, len(idxs))
			if bd := breakdown(idxs, m.actions, false); bd != "" {
				text += "  " + bd
			}
			style := m.th.header
			if selected {
				style = m.th.selected
			}
			lines = append(lines, clip(mark+style.Render(text), w))
		case rowItem:
			f := m.findings[r.findingIdx]
			style := m.th.item
			if selected {
				style = m.th.selected
			}
			label := f.AgentLabel
			if m.group == groupByType {
				// The group header already names the type; don't repeat it on the item.
				label = strings.TrimSuffix(label, ": "+f.Group)
			}
			text := fmt.Sprintf("%s  %s  ·%d", label, f.Title, f.Score)
			badge := m.th.suggestBadge(m.actions[r.findingIdx], f.Selection)
			lines = append(lines, clip(mark+"  "+style.Render(text)+"  "+badge, w))
		case rowSubmit:
			lines = append(lines, m.th.dim.Render(strings.Repeat("─", w)))
			style := m.th.footer
			if selected {
				style = m.th.selected
			}
			text := "Submit  " + breakdown(allIdx(len(m.findings)), m.actions, true)
			lines = append(lines, clip(mark+style.Render(text), w))
		}
	}
	return strings.Join(lines, "\n")
}

func (m *model) viewTable() string {
	w, _ := m.layout()
	return m.th.border.Border(lipgloss.RoundedBorder()).Width(w).Render(m.tableInner(w))
}

func (m *model) viewSplit() string {
	w, _ := m.layout()
	left := m.th.border.Border(lipgloss.RoundedBorder()).Width(w).Render(m.tableInner(w))
	right := m.detailPane()
	return lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)
}

func allIdx(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}
