package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/valian-ca/homebrew-tools/internal/review-triage/contract"
)

func (m *model) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		for _, a := range m.actions {
			if a == nil {
				return m, nil // defensive: never finalize with undecided findings
			}
		}
		m.finalize()
		return m, tea.Quit
	case "esc", "q":
		m.screen = screenTable
		return m, nil
	}
	return m, nil
}

func (m *model) viewConfirm() string {
	var fix, skip, disc, noPrompt, undecided int
	for i := range m.findings {
		switch {
		case m.actions[i] == nil:
			undecided++
		case *m.actions[i] == contract.ActionFix:
			fix++
		case *m.actions[i] == contract.ActionSkip:
			skip++
		case *m.actions[i] == contract.ActionDiscuss:
			disc++
			if strings.TrimSpace(m.prompts[i]) == "" {
				noPrompt++
			}
		}
	}

	var b strings.Builder
	b.WriteString(m.th.header.Render("Plan") + "\n\n")
	b.WriteString(fmt.Sprintf("  %d fix\n", fix))
	discLine := fmt.Sprintf("  %d discuss", disc)
	if noPrompt > 0 {
		discLine += fmt.Sprintf("  (%d without prompt)", noPrompt)
	}
	b.WriteString(discLine + "\n")
	b.WriteString(fmt.Sprintf("  %d skip\n", skip))

	if undecided > 0 {
		b.WriteString("\n" + m.th.dim.Render(fmt.Sprintf("%d finding(s) still undecided", undecided)) + "\n")
		b.WriteString("\n[Esc] back")
	} else {
		b.WriteString("\n[Enter] ship   [Esc] back")
	}
	return boxCenter(b.String(), m.width, m.height)
}

func boxCenter(content string, w, h int) string {
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(1, 3).Render(content)
	if w <= 0 || h <= 0 {
		return box
	}
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, box)
}
