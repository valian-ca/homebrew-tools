package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/valian-ca/homebrew-tools/internal/review-triage/contract"
)

func sampleInput() *contract.Input {
	fix := contract.ActionFix
	return &contract.Input{
		SchemaVersion: 1,
		Findings: []contract.Finding{
			{ID: 1, Title: "A", Group: "comments", AgentLabel: "#1: comments", Score: 90, Language: "go", CodeExcerpt: "a := 1", ProposedFix: contract.ProposedFix{Code: "a := 2"}, Selection: &fix},
			{ID: 2, Title: "B", Group: "comments", AgentLabel: "#2: comments", Score: 50, Language: "go", CodeExcerpt: "b := 1", ProposedFix: contract.ProposedFix{Code: "b := 2"}},
			{ID: 3, Title: "C", Group: "bugs", AgentLabel: "#3: bugs", Score: 80, Language: "go", CodeExcerpt: "c := 1", ProposedFix: contract.ProposedFix{Code: "c := 2"}},
		},
	}
}

func newTestModel(t *testing.T, noColor bool, width int) *model {
	t.Helper()
	m := newModel(sampleInput(), noColor)
	m.Update(tea.WindowSizeMsg{Width: width, Height: 40})
	return m
}

func key(s string) tea.KeyMsg {
	switch s {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "ctrl+s":
		return tea.KeyMsg{Type: tea.KeyCtrlS}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func send(m *model, s string) { m.Update(key(s)) }

func actionAt(m *model, findingIdx int) string {
	if m.actions[findingIdx] == nil {
		return "?"
	}
	return string(*m.actions[findingIdx])
}

// AC 1: grouped by type, score desc within group, headers present.
func TestTableRowsGroupedAndSorted(t *testing.T) {
	m := newTestModel(t, true, 120)
	want := []struct {
		kind rowKind
		key  string
		id   int
	}{
		{rowHeader, "comments", 0},
		{rowItem, "comments", 1},
		{rowItem, "comments", 2},
		{rowHeader, "bugs", 0},
		{rowItem, "bugs", 3},
		{rowSubmit, "", 0},
	}
	if len(m.rows) != len(want) {
		t.Fatalf("rows: got %d, want %d (%+v)", len(m.rows), len(want), m.rows)
	}
	for i, w := range want {
		r := m.rows[i]
		if r.kind != w.kind || (w.kind == rowHeader && r.groupKey != w.key) {
			t.Fatalf("row %d: got %+v, want %+v", i, r, w)
		}
		if w.kind == rowItem && m.findings[r.findingIdx].ID != w.id {
			t.Fatalf("row %d: got finding id %d, want %d", i, m.findings[r.findingIdx].ID, w.id)
		}
	}
}

// AC 2: f on an item sets fix and advances; f on a header bulk-sets the group.
func TestActionKeysItemAndHeader(t *testing.T) {
	m := newTestModel(t, true, 120)
	m.cursor = 1 // item id1
	send(m, "f")
	if actionAt(m, 0) != "fix" {
		t.Fatalf("f on item: action = %s, want fix", actionAt(m, 0))
	}
	if m.cursor != 2 {
		t.Fatalf("f should advance cursor to 2, got %d", m.cursor)
	}

	m = newTestModel(t, true, 120)
	m.cursor = 0 // comments header
	send(m, "s")
	if actionAt(m, 0) != "skip" || actionAt(m, 1) != "skip" {
		t.Fatalf("s on header: comments items = %s,%s want skip,skip", actionAt(m, 0), actionAt(m, 1))
	}
	if actionAt(m, 2) != "?" {
		t.Fatalf("bugs item should be untouched, got %s", actionAt(m, 2))
	}
	if m.rows[m.cursor].kind != rowHeader {
		t.Fatalf("s on header should advance to next header, cursor on %v", m.rows[m.cursor].kind)
	}
}

// AC 3: d on item opens Discuss; Tab commits discuss + prompt. d on header
// bulk-sets discuss with empty prompt and does not open the textarea.
func TestDiscussItemAndHeader(t *testing.T) {
	m := newTestModel(t, true, 120)
	m.cursor = 1
	send(m, "d")
	if m.screen != screenDiscuss {
		t.Fatalf("d on item should open Discuss, screen=%v", m.screen)
	}
	m.ta.SetValue("why not optional chaining")
	send(m, "tab")
	if m.screen != screenTable {
		t.Fatalf("Tab should return to table, screen=%v", m.screen)
	}
	if actionAt(m, 0) != "discuss" || m.prompts[0] != "why not optional chaining" {
		t.Fatalf("discuss not committed: action=%s prompt=%q", actionAt(m, 0), m.prompts[0])
	}

	m = newTestModel(t, true, 120)
	m.cursor = 0
	send(m, "d")
	if m.screen != screenTable {
		t.Fatalf("d on header must not open Discuss, screen=%v", m.screen)
	}
	if actionAt(m, 0) != "discuss" || actionAt(m, 1) != "discuss" {
		t.Fatalf("d on header should bulk-discuss the group, got %s,%s", actionAt(m, 0), actionAt(m, 1))
	}
	if m.prompts[0] != "" || m.prompts[1] != "" {
		t.Fatalf("header discuss prompts should be empty, got %q,%q", m.prompts[0], m.prompts[1])
	}
}

// AC 3 (cont.): Esc cancels a fresh Discuss and rolls the action back.
func TestDiscussCancelRollback(t *testing.T) {
	m := newTestModel(t, true, 120)
	m.cursor = 1
	send(m, "d")
	m.ta.SetValue("text")
	send(m, "esc")
	if m.screen != screenTable {
		t.Fatalf("esc should return to table")
	}
	if actionAt(m, 0) != "?" {
		t.Fatalf("fresh discuss cancelled should leave action undecided, got %s", actionAt(m, 0))
	}
}

// AC 4: Tab accepts the suggested selection; refused when selection is nil.
func TestTabAcceptsSelection(t *testing.T) {
	m := newTestModel(t, true, 120)
	m.cursor = 1 // id1 has selection=fix
	send(m, "tab")
	if actionAt(m, 0) != "fix" || m.cursor != 2 {
		t.Fatalf("Tab on item with selection: action=%s cursor=%d", actionAt(m, 0), m.cursor)
	}

	m = newTestModel(t, true, 120)
	m.cursor = 2 // id2 has nil selection
	send(m, "tab")
	if actionAt(m, 1) != "?" {
		t.Fatalf("Tab on nil-selection item should not set an action, got %s", actionAt(m, 1))
	}
	if m.cursor != 2 {
		t.Fatalf("Tab on nil-selection item should not advance, cursor=%d", m.cursor)
	}
	if m.footer == "" {
		t.Fatalf("expected soft feedback in footer")
	}
}

// AC 5: g cycles through the four group-by dimensions.
func TestGroupCycle(t *testing.T) {
	m := newTestModel(t, true, 120)
	want := []groupBy{groupByScore, groupByAction, groupByFile, groupByType}
	for _, w := range want {
		send(m, "g")
		if m.group != w {
			t.Fatalf("cycle: got %v want %v", m.group, w)
		}
	}
}

// AC 10: Ctrl+S refused while undecided; Confirm shown when all decided.
func TestSubmitGate(t *testing.T) {
	m := newTestModel(t, true, 120)
	send(m, "ctrl+s")
	if m.screen != screenTable || m.footer == "" {
		t.Fatalf("Ctrl+S with undecided should refuse, screen=%v footer=%q", m.screen, m.footer)
	}
	for i := range m.findings {
		setAction(&m.actions[i], contract.ActionFix)
	}
	send(m, "ctrl+s")
	if m.screen != screenConfirm {
		t.Fatalf("Ctrl+S all-decided should open Confirm, screen=%v", m.screen)
	}
}

// AC 11: Confirm Enter finalizes — one decision per finding, prompt iff discuss.
func TestConfirmFinalize(t *testing.T) {
	m := newTestModel(t, true, 120)
	setAction(&m.actions[0], contract.ActionFix)
	setAction(&m.actions[1], contract.ActionDiscuss)
	m.prompts[1] = "let's talk"
	setAction(&m.actions[2], contract.ActionSkip)
	send(m, "ctrl+s")
	send(m, "enter")
	if m.outcome != OutcomeSubmit {
		t.Fatalf("expected submit outcome, got %v", m.outcome)
	}
	if len(m.decisions) != 3 {
		t.Fatalf("expected 3 decisions, got %d", len(m.decisions))
	}
	byID := map[int]contract.Decision{}
	for _, d := range m.decisions {
		byID[d.ID] = d
	}
	if byID[1].DiscussPrompt != nil {
		t.Fatalf("fix decision must not carry a prompt")
	}
	if byID[2].DiscussPrompt == nil || *byID[2].DiscussPrompt != "let's talk" {
		t.Fatalf("discuss decision must carry its prompt, got %v", byID[2].DiscussPrompt)
	}
}

// AC 12: Ctrl+C opens quit confirm; y cancels with no decisions.
func TestQuitConfirm(t *testing.T) {
	m := newTestModel(t, true, 120)
	send(m, "ctrl+c")
	if !m.quitting {
		t.Fatalf("Ctrl+C should open quit confirm")
	}
	send(m, "y")
	if m.outcome != OutcomeCancel {
		t.Fatalf("y should cancel, outcome=%v", m.outcome)
	}
	if m.decisions != nil {
		t.Fatalf("cancel must not produce decisions")
	}
}

// AC 9: split engages at >=160; Enter focuses Detail pane, Esc returns focus;
// crossing the threshold re-lays-out without losing the cursor.
func TestSplitFocusAndResize(t *testing.T) {
	m := newTestModel(t, true, 161)
	if !m.split() {
		t.Fatalf("expected split at width 161")
	}
	m.cursor = 1
	send(m, "enter")
	if !m.detailFocused {
		t.Fatalf("Enter in split should focus Detail pane")
	}
	send(m, "esc")
	if m.detailFocused {
		t.Fatalf("Esc should return focus to Table")
	}
	cur := m.cursor
	m.Update(tea.WindowSizeMsg{Width: 159, Height: 40})
	if m.split() {
		t.Fatalf("expected non-split at width 159")
	}
	if m.detailFocused {
		t.Fatalf("collapsing split should clear detailFocused")
	}
	if m.cursor != cur {
		t.Fatalf("resize lost the cursor: %d -> %d", cur, m.cursor)
	}
}

// AC 13: NO_COLOR yields output with no ANSI escapes and plain badges.
func TestNoColorRendering(t *testing.T) {
	m := newTestModel(t, true, 120)
	view := m.viewTable()
	if strings.Contains(view, "\x1b[") {
		t.Fatalf("NO_COLOR view must contain no ANSI escapes")
	}
	a := contract.ActionFix
	if got := m.th.badge(&a); got != "[FIX]" {
		t.Fatalf("NO_COLOR badge should be plain [FIX], got %q", got)
	}
}

// Suggestion badge: undecided shows the pending selection so Tab is legible.
func TestSuggestBadge(t *testing.T) {
	th := newTheme(true)
	skip := contract.ActionSkip
	fix := contract.ActionFix
	if got := th.suggestBadge(nil, &skip); got != "[SKIP?]" {
		t.Fatalf("undecided + skip selection => %q, want [SKIP?]", got)
	}
	if got := th.suggestBadge(nil, nil); got != "[?]" {
		t.Fatalf("undecided + no selection => %q, want [?]", got)
	}
	if got := th.suggestBadge(&fix, &skip); got != "[FIX]" {
		t.Fatalf("decided fix => %q, want firm [FIX]", got)
	}
}

// AC 6: narrow Enter drills into the Detail modal.
func TestNarrowEnterOpensDetail(t *testing.T) {
	m := newTestModel(t, true, 120)
	m.cursor = 1
	send(m, "enter")
	if m.screen != screenDetail {
		t.Fatalf("narrow Enter should open Detail modal, screen=%v", m.screen)
	}
	send(m, "esc")
	if m.screen != screenTable {
		t.Fatalf("Esc should return to Table, screen=%v", m.screen)
	}
}

// Empty findings short-circuit to a submit with no decisions (Robustesse note).
func TestRunEmptyFindings(t *testing.T) {
	res, err := Run(&contract.Input{SchemaVersion: 1, Findings: nil})
	if err != nil {
		t.Fatalf("Run empty: %v", err)
	}
	if res.Outcome != OutcomeSubmit || len(res.Decisions) != 0 {
		t.Fatalf("empty findings should submit zero decisions, got %+v", res)
	}
}
