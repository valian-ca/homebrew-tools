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

// scoreBucket maps a 0-100 score to a fixed band label.
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

// groupKeyFor returns the group key of finding i under the given dimension.
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

// groupOrder returns the ordered list of group keys for the dimension. Score
// and action use fixed orders; type and file follow first-appearance order.
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

// buildRows flattens findings into header+item rows for the given dimension,
// items sorted by score descending within each group, then a trailing submit
// row.
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

// breakdown counts the actions over the given finding indices and renders the
// non-zero buckets in fix · discuss · skip · ? order. When includeZero is
// true (the submit footer) every bucket is shown.
func breakdown(idxs []int, actions []*contract.Action, includeZero bool) string {
	var fix, skip, disc, undecided int
	for _, i := range idxs {
		switch {
		case actions[i] == nil:
			undecided++
		case *actions[i] == contract.ActionFix:
			fix++
		case *actions[i] == contract.ActionSkip:
			skip++
		case *actions[i] == contract.ActionDiscuss:
			disc++
		}
	}
	var parts []string
	add := func(n int, label string) {
		if n > 0 || includeZero {
			parts = append(parts, fmt.Sprintf("%d %s", n, label))
		}
	}
	add(fix, "fix")
	add(disc, "discuss")
	add(skip, "skip")
	add(undecided, "?")
	return strings.Join(parts, " · ")
}
