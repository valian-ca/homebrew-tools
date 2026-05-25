package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/valian-ca/homebrew-tools/internal/review-triage/contract"
	"github.com/valian-ca/homebrew-tools/internal/review-triage/highlight"
)

func (m *model) updateDetail(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		m.screen = screenTable
		return m, nil
	case "left", "h":
		m.detailStep(-1)
		return m, nil
	case "right", "l":
		m.detailStep(1)
		return m, nil
	case "g":
		m.vp.GotoTop()
		return m, nil
	case "G":
		m.vp.GotoBottom()
		return m, nil
	case "f":
		m.applyActionDetail(contract.ActionFix)
		return m, nil
	case "s":
		m.applyActionDetail(contract.ActionSkip)
		return m, nil
	case "d":
		if idx := m.currentFindingIdx(); idx >= 0 {
			return m.openDiscuss(idx, screenDetail)
		}
		return m, nil
	case "tab":
		m.applyTabDetail()
		return m, nil
	}
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

// refreshDetail re-renders the Detail viewport for the finding under the
// cursor and scrolls it back to the top. A cursor on a header or the submit
// row clears the pane.
func (m *model) refreshDetail() {
	idx := m.currentFindingIdx()
	if idx < 0 {
		m.vp.SetContent("")
		return
	}
	_, w := m.layout()
	m.vp.SetContent(m.detailBody(idx, w))
	m.vp.GotoTop()
}

// detailStep moves the cursor to the previous/next rowItem, skipping headers,
// so the Detail can page through findings without returning to the Table.
func (m *model) detailStep(dir int) {
	for i := m.cursor + dir; i >= 0 && i < len(m.rows); i += dir {
		if m.rows[i].kind == rowItem {
			m.cursor = i
			m.refreshDetail()
			return
		}
	}
}

func (m *model) applyActionDetail(a contract.Action) {
	idx := m.currentFindingIdx()
	if idx < 0 {
		return
	}
	setAction(&m.actions[idx], a)
	m.detailStep(1)
}

func (m *model) applyTabDetail() {
	idx := m.currentFindingIdx()
	if idx < 0 {
		return
	}
	sel := m.findings[idx].Selection
	if sel == nil {
		return
	}
	setAction(&m.actions[idx], *sel)
	m.detailStep(1)
}

// detailBody renders the full description of one finding, wrapped/clipped to w.
// Order: heading, location, explanation, proposed fix, then the diff. The
// proposed fix precedes the diff (a deliberate departure from output-format.md,
// which lists it last) so the reader sees the intended change before the
// mechanics. Colour is reserved for emphasis (type, score, badge, diff markers,
// code); the prose is grey.
func (m *model) detailBody(idx, w int) string {
	f := m.findings[idx]
	var b strings.Builder

	// Heading: grey, with the finding's type in white.
	var heading string
	if prefix := strings.TrimSuffix(f.AgentLabel, ": "+f.Group); prefix != f.AgentLabel {
		heading = m.th.item.Render(prefix+": ") + m.th.white.Render(f.Group) + m.th.item.Render(" · "+f.Title)
	} else {
		heading = m.th.item.Render(f.AgentLabel + " · " + f.Title)
	}
	b.WriteString(ansi.Truncate(heading, w, "…") + "\n")

	// Location line: grey, with the score in white.
	loc := fmt.Sprintf("%s:%d-%d · score ", f.File, f.LineStart, f.LineEnd)
	b.WriteString(m.th.item.Render(loc) + m.th.white.Render(fmt.Sprintf("%d", f.Score)) +
		m.th.item.Render(" · ") + m.th.suggestBadge(m.actions[idx], f.Selection) + "\n\n")

	if f.Explanation != "" {
		b.WriteString(m.th.item.Width(w).Render(f.Explanation) + "\n\n")
	}
	if f.ProposedFix.Explanation != "" {
		b.WriteString(m.th.header.Render("Proposed:") + "\n")
		b.WriteString(m.th.item.Width(w).Render(f.ProposedFix.Explanation) + "\n\n")
	}
	b.WriteString(m.renderDiff(f, w))
	return b.String()
}

func (m *model) renderDiff(f contract.Finding, w int) string {
	lines := highlight.Diff(f.CodeExcerpt, f.ProposedFix.Code)
	var out []string
	for _, ln := range lines {
		marker, st := " ", lipgloss.NewStyle()
		switch ln.Kind {
		case highlight.KindAdd:
			marker, st = "+", m.th.addLine
		case highlight.KindDelete:
			marker, st = "-", m.th.delLine
		}
		gutter := marker + " "
		if !m.noColor && marker != " " {
			gutter = st.Render(marker) + " "
		}
		out = append(out, ansi.Truncate(gutter+highlight.Code(ln.Content, f.Language, m.noColor), w, "…"))
	}
	return strings.Join(out, "\n")
}

func (m *model) detailHint() string {
	return m.th.dim.Render("[f/s/d] action  [Tab] keep  [←/→] finding  [j/k] scroll  [Esc] back  [?] help")
}

// chromeBox wraps content in the same window frame as the Table: an inverse
// title bar on top, inside a rounded border, all sized to exactly w columns.
func (m *model) chromeBox(title, body string, w int) string {
	bar := m.th.titleBar.Width(w).Render(ansi.Truncate(title, w, ""))
	return m.th.border.Border(lipgloss.RoundedBorder()).Width(w).Render(bar + "\n" + body)
}

func (m *model) viewDetail() string {
	_, w := m.layout()
	return m.chromeBox(" Detail ", m.vp.View(), w)
}

func (m *model) detailPane() string {
	_, w := m.layout()
	title := " Detail "
	if m.detailFocused {
		title = " Detail · focused (Esc to return) "
	}
	body := m.vp.View()
	if strings.TrimSpace(body) == "" {
		body = m.th.dim.Render("(no finding selected)")
	}
	return m.chromeBox(title, body, w)
}
