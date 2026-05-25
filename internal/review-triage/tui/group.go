package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/valian-ca/homebrew-tools/internal/review-triage/contract"
)

type groupBy int

const (
	groupByType groupBy = iota
	groupByScore
	groupByAction
	groupByFile
	groupByCount // sentinel for cycling
)

func (g groupBy) label() string {
	switch g {
	case groupByType:
		return "type"
	case groupByScore:
		return "score"
	case groupByAction:
		return "action"
	case groupByFile:
		return "file"
	default:
		return "type"
	}
}

type rowKind int

const (
	rowHeader rowKind = iota
	rowItem
	rowSubmit
)

type row struct {
	kind       rowKind
	groupKey   string
	findingIdx int // valid only for rowItem
}

func scoreBucket(score int) string {
	switch {
	case score >= 100:
		return "100"
	case score > 75:
		return "<100"
	case score > 50:
		return "≤75"
	case score > 25:
		return "≤50"
	case score > 0:
		return "≤25"
	default:
		return "0"
	}
}

func groupKeyFor(g groupBy, f contract.Finding, action *contract.Action) string {
	switch g {
	case groupByScore:
		return scoreBucket(f.Score)
	case groupByAction:
		if action == nil {
			return "?"
		}
		return string(*action)
	case groupByFile:
		return f.File
	default:
		return f.Group
	}
}

func groupOrder(g groupBy, findings []contract.Finding, actions []*contract.Action) []string {
	switch g {
	case groupByScore:
		return filterPresent([]string{"100", "<100", "≤75", "≤50", "≤25", "0"}, g, findings, actions)
	case groupByAction:
		return filterPresent([]string{"?", "fix", "skip", "discuss"}, g, findings, actions)
	default:
		var order []string
		seen := map[string]bool{}
		for i, f := range findings {
			key := groupKeyFor(g, f, actions[i])
			if !seen[key] {
				seen[key] = true
				order = append(order, key)
			}
		}
		return order
	}
}

func filterPresent(candidates []string, g groupBy, findings []contract.Finding, actions []*contract.Action) []string {
	present := map[string]bool{}
	for i, f := range findings {
		present[groupKeyFor(g, f, actions[i])] = true
	}
	var out []string
	for _, c := range candidates {
		if present[c] {
			out = append(out, c)
		}
	}
	return out
}

func buildRows(g groupBy, findings []contract.Finding, actions []*contract.Action) []row {
	byKey := map[string][]int{}
	for i, f := range findings {
		key := groupKeyFor(g, f, actions[i])
		byKey[key] = append(byKey[key], i)
	}
	var rows []row
	for _, key := range groupOrder(g, findings, actions) {
		idxs := byKey[key]
		sort.SliceStable(idxs, func(a, b int) bool {
			return findings[idxs[a]].Score > findings[idxs[b]].Score
		})
		rows = append(rows, row{kind: rowHeader, groupKey: key})
		for _, idx := range idxs {
			rows = append(rows, row{kind: rowItem, groupKey: key, findingIdx: idx})
		}
	}
	rows = append(rows, row{kind: rowSubmit})
	return rows
}

type actionCounts struct{ fix, discuss, skip, undecided int }

func tally(idxs []int, actions []*contract.Action) actionCounts {
	var c actionCounts
	for _, i := range idxs {
		switch {
		case actions[i] == nil:
			c.undecided++
		case *actions[i] == contract.ActionFix:
			c.fix++
		case *actions[i] == contract.ActionSkip:
			c.skip++
		case *actions[i] == contract.ActionDiscuss:
			c.discuss++
		}
	}
	return c
}

// breakdown renders the non-zero buckets in fix · discuss · skip · ? order;
// includeZero (the submit footer) shows every bucket.
func breakdown(idxs []int, actions []*contract.Action, includeZero bool) string {
	c := tally(idxs, actions)
	var parts []string
	add := func(n int, label string) {
		if n > 0 || includeZero {
			parts = append(parts, fmt.Sprintf("%d %s", n, label))
		}
	}
	add(c.fix, "fix")
	add(c.discuss, "discuss")
	add(c.skip, "skip")
	add(c.undecided, "?")
	return strings.Join(parts, " · ")
}
