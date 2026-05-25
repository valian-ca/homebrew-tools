package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m *model) View() string {
	if m.width == 0 && m.height == 0 {
		return ""
	}
	if m.quitting {
		return m.viewQuit()
	}
	if m.help {
		return m.viewHelp()
	}
	switch m.screen {
	case screenDetail:
		return m.frame(m.viewDetail(), m.detailHint())
	case screenDiscuss:
		return m.viewDiscuss()
	case screenConfirm:
		return m.viewConfirm()
	default:
		if m.split() {
			return m.frame(m.viewSplit(), m.helpHint())
		}
		return m.frame(m.viewTable(), m.helpHint())
	}
}

// frame places body at the top and pins the status/help line to the bottom of
// the screen, so the help hint stays put no matter how the body height varies.
func (m *model) frame(body, hint string) string {
	bottom := hint
	if m.footer != "" {
		bottom = m.th.dim.Render(m.footer) + "\n" + hint
	}
	if m.height <= 0 {
		return body + "\n\n" + bottom
	}
	gap := m.height - lipgloss.Height(body) - lipgloss.Height(bottom)
	if gap < 1 {
		gap = 1
	}
	return body + strings.Repeat("\n", gap) + bottom
}

func (m *model) helpHint() string {
	return m.th.dim.Render("[f/s/d] action  [Tab] keep  [g] group  [j/k] nav  [Enter] detail  [Ctrl+S] submit  [?] help")
}

func (m *model) viewQuit() string {
	return boxCenter("Quit?\n\nAll decisions will be lost.\n\n[y] quit   [N] back", m.width, m.height)
}

func (m *model) viewHelp() string {
	var b strings.Builder
	b.WriteString(m.th.header.Render("Keys") + "\n\n")
	lines := []string{
		"j / k / ↑ / ↓   move cursor",
		"f / s           set fix / skip (item or group header)",
		"d               discuss (item: prompt; header: bulk, empty prompt)",
		"Tab             accept the suggested selection",
		"g               cycle group-by (type / score / action / file)",
		"Enter           open Detail (or focus Detail pane in split)",
		"Ctrl+S          submit (refused while any finding is undecided)",
		"Ctrl+C / q      quit (with confirmation)",
		"← / →           previous / next finding (in Detail)",
		"Esc             back / cancel",
		"?               toggle this help",
	}
	for _, l := range lines {
		b.WriteString("  " + l + "\n")
	}
	b.WriteString("\n" + m.th.dim.Render("[?] or [Esc] to close"))
	return boxCenter(b.String(), m.width, m.height)
}
