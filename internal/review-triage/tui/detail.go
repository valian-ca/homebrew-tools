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

// detailBody puts the proposed fix before the diff — a deliberate departure
// from output-format.md (which lists it last) so the reader sees the intended
// change before the mechanics.
func (m *model) detailBody(idx, w int) string {
	f := m.findings[idx]
	var b strings.Builder

	var heading string
	if prefix := strings.TrimSuffix(f.AgentLabel, ": "+f.Group); prefix != f.AgentLabel {
		heading = m.th.item.Render(prefix+": ") + m.th.white.Render(f.Group) + m.th.item.Render(" · "+f.Title)
	} else {
		heading = m.th.item.Render(f.AgentLabel + " · " + f.Title)
	}
	b.WriteString(ansi.Truncate(heading, w, "…") + "\n")

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
	lines := highlight.Diff(f.CodeExcerpt, f.ProposedFix.Code, f.LineStart, f.Language)
	if len(lines) == 0 {
		return ""
	}
	maxNum := f.LineStart
	for _, l := range lines {
		maxNum = max(maxNum, l.OldNum, l.NewNum)
	}
	numW := len(fmt.Sprintf("%d", maxNum))

	var out []string
	for _, l := range lines {
		out = append(out, m.renderDiffLine(l, numW, w))
	}
	return strings.Join(out, "\n")
}

func (m *model) renderDiffLine(l highlight.Line, numW, w int) string {
	num := func(n int) string {
		if n == 0 {
			return strings.Repeat(" ", numW)
		}
		return fmt.Sprintf("%*d", numW, n)
	}
	gutterText := num(l.OldNum) + " " + num(l.NewNum) + " "
	gutterW := numW*2 + 2
	// The +/- markers are redundant with the red/green backgrounds, so they
	// only appear under NO_COLOR, where the background can't carry the signal.
	if m.noColor {
		marker := " "
		switch l.Kind {
		case highlight.KindAdd:
			marker = "+"
		case highlight.KindDelete:
			marker = "-"
		}
		gutterText += marker + " "
		gutterW += 2
	}
	contentW := w - gutterW
	if contentW < 4 {
		contentW = 4
	}
	return m.th.diffNum.Render(gutterText) + m.renderSegs(l, contentW)
}

func (m *model) renderSegs(l highlight.Line, contentW int) string {
	if m.noColor {
		var sb strings.Builder
		for _, s := range l.Segs {
			sb.WriteString(s.Text)
		}
		return ansi.Truncate(sb.String(), contentW, "…")
	}
	baseBg, emphBg := diffBackgrounds(l.Kind)
	var sb strings.Builder
	used := 0
	for _, s := range l.Segs {
		if used >= contentW {
			break
		}
		text := s.Text
		if used+lipgloss.Width(text) > contentW {
			text = ansi.Truncate(text, contentW-used, "…")
		}
		sb.WriteString(segStyle(s.Color, bgOf(s.Emphasis, baseBg, emphBg)).Render(text))
		used += lipgloss.Width(text)
	}
	if used < contentW && baseBg != "" {
		sb.WriteString(lipgloss.NewStyle().Background(lipgloss.Color(baseBg)).Render(strings.Repeat(" ", contentW-used)))
	}
	return sb.String()
}

func segStyle(fg, bg string) lipgloss.Style {
	st := lipgloss.NewStyle()
	if fg != "" {
		st = st.Foreground(lipgloss.Color(fg))
	}
	if bg != "" {
		st = st.Background(lipgloss.Color(bg))
	}
	return st
}

func bgOf(emphasis bool, base, emph string) string {
	if emphasis {
		return emph
	}
	return base
}

func diffBackgrounds(k highlight.LineKind) (base, emph string) {
	switch k {
	case highlight.KindAdd:
		return "22", "29"
	case highlight.KindDelete:
		return "52", "88"
	default:
		return "", ""
	}
}

func (m *model) detailHint() string {
	return m.th.dim.Render("[f/s/d] action  [Tab] keep  [←/→] finding  [j/k] scroll  [Esc] back  [?] help")
}

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
