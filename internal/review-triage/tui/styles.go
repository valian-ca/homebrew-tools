package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/valian-ca/homebrew-tools/internal/review-triage/contract"
)

// theme carries the lipgloss styles. When noColor is set every style is the
// zero (identity) style, so rendered output contains no ANSI escapes — the
// $NO_COLOR contract (https://no-color.org).
type theme struct {
	noColor   bool
	titleBar  lipgloss.Style
	header    lipgloss.Style
	selected  lipgloss.Style
	white     lipgloss.Style
	item      lipgloss.Style
	cursor    lipgloss.Style
	dim       lipgloss.Style
	footer    lipgloss.Style
	border    lipgloss.Style
	diffNum   lipgloss.Style
	badgeFix  lipgloss.Style
	badgeSkip lipgloss.Style
	badgeDisc lipgloss.Style
	badgeNone lipgloss.Style
}

func newTheme(noColor bool) theme {
	if noColor {
		return theme{noColor: true}
	}
	return theme{
		titleBar:  lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")).Background(lipgloss.Color("63")),
		header:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63")),
		selected:  lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")),
		white:     lipgloss.NewStyle().Foreground(lipgloss.Color("231")),
		item:      lipgloss.NewStyle().Foreground(lipgloss.Color("250")),
		cursor:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")),
		dim:       lipgloss.NewStyle().Faint(true),
		footer:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39")),
		border:    lipgloss.NewStyle().Foreground(lipgloss.Color("63")),
		diffNum:   lipgloss.NewStyle().Foreground(lipgloss.Color("240")),
		badgeFix:  lipgloss.NewStyle().Foreground(lipgloss.Color("42")),
		badgeSkip: lipgloss.NewStyle().Foreground(lipgloss.Color("244")),
		badgeDisc: lipgloss.NewStyle().Foreground(lipgloss.Color("214")),
		badgeNone: lipgloss.NewStyle().Foreground(lipgloss.Color("203")),
	}
}

func badgeText(a *contract.Action) string {
	switch {
	case a == nil:
		return "[?]"
	case *a == contract.ActionFix:
		return "[FIX]"
	case *a == contract.ActionSkip:
		return "[SKIP]"
	case *a == contract.ActionDiscuss:
		return "[DISCUSS]"
	default:
		return "[?]"
	}
}

// suggestBadge renders the row badge: the firm decision (coloured by action)
// once one is made, otherwise the pending suggestion as "[FIX?]" — always in
// red — so the user sees what Tab would accept and that a decision is still
// owed. "[?]" when there is no suggestion at all.
func (t theme) suggestBadge(action, selection *contract.Action) string {
	if action != nil {
		return t.badge(action)
	}
	text := "[?]"
	if selection != nil {
		text = "[" + strings.ToUpper(string(*selection)) + "?]"
	}
	if t.noColor {
		return text
	}
	return t.badgeNone.Render(text)
}

func (t theme) badge(a *contract.Action) string {
	text := badgeText(a)
	if t.noColor {
		return text
	}
	switch {
	case a == nil:
		return t.badgeNone.Render(text)
	case *a == contract.ActionFix:
		return t.badgeFix.Render(text)
	case *a == contract.ActionSkip:
		return t.badgeSkip.Render(text)
	case *a == contract.ActionDiscuss:
		return t.badgeDisc.Render(text)
	default:
		return text
	}
}
