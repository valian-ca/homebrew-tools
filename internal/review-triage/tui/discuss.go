package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/valian-ca/homebrew-tools/internal/review-triage/contract"
)

func (m *model) openDiscuss(idx int, ret screen) (tea.Model, tea.Cmd) {
	m.discussIdx = idx
	m.discussReturn = ret
	m.discussPrev = m.actions[idx]
	m.discussFresh = m.actions[idx] == nil || *m.actions[idx] != contract.ActionDiscuss
	m.ta.SetValue(m.prompts[idx])
	m.screen = screenDiscuss
	return m, tea.Batch(m.ta.Focus(), textarea.Blink)
}

func (m *model) updateDiscuss(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "tab":
		idx := m.discussIdx
		m.prompts[idx] = m.ta.Value()
		setAction(&m.actions[idx], contract.ActionDiscuss)
		m.ta.Blur()
		m.screen = m.discussReturn
		m.advanceAfterDiscuss()
		return m, nil
	case "esc":
		if m.discussFresh {
			m.actions[m.discussIdx] = m.discussPrev
		}
		m.ta.Blur()
		m.screen = m.discussReturn
		return m, nil
	}
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	return m, cmd
}

func (m *model) advanceAfterDiscuss() {
	if m.discussReturn == screenDetail {
		m.detailStep(1)
		return
	}
	m.advanceToNextItem()
}

func (m *model) viewDiscuss() string {
	f := m.findings[m.discussIdx]
	taW := m.ta.Width()
	var b strings.Builder
	b.WriteString(m.th.header.Render("Discuss") + m.th.dim.Render(" · "+ansi.Truncate(f.Title, taW-10, "…")) + "\n")
	b.WriteString(m.th.item.Render("Note for the discussion (read verbatim by the LLM):") + "\n\n")
	b.WriteString(m.th.border.Border(lipgloss.NormalBorder()).Render(m.ta.View()) + "\n\n")
	b.WriteString(m.th.dim.Render("[Tab] save   [Esc] cancel   [Enter] newline"))
	return boxCenter(b.String(), m.width, m.height)
}
