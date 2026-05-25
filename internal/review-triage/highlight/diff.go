package highlight

import (
	"strings"

	"github.com/hexops/gotextdiff"
	"github.com/hexops/gotextdiff/myers"
	"github.com/hexops/gotextdiff/span"
)

// LineKind tags a diff line as unchanged context, an addition, or a deletion.
type LineKind int

const (
	KindContext LineKind = iota
	KindAdd
	KindDelete
)

// Line is one rendered diff line: its kind and its (newline-stripped) text.
type Line struct {
	Kind    LineKind
	Content string
}

// Diff computes a unified line diff between oldCode and newCode. An empty
// oldCode yields all-add lines (pure addition); an empty newCode yields
// all-delete lines (pure deletion).
func Diff(oldCode, newCode string) []Line {
	o := ensureTrailingNewline(oldCode)
	n := ensureTrailingNewline(newCode)
	edits := myers.ComputeEdits(span.URIFromPath("a"), o, n)
	unified := gotextdiff.ToUnified("a", "b", o, edits)

	var lines []Line
	for _, hunk := range unified.Hunks {
		for _, ln := range hunk.Lines {
			content := strings.TrimSuffix(ln.Content, "\n")
			switch ln.Kind {
			case gotextdiff.Insert:
				lines = append(lines, Line{Kind: KindAdd, Content: content})
			case gotextdiff.Delete:
				lines = append(lines, Line{Kind: KindDelete, Content: content})
			default:
				lines = append(lines, Line{Kind: KindContext, Content: content})
			}
		}
	}
	return lines
}

func ensureTrailingNewline(s string) string {
	if s == "" || strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}
