package highlight

import (
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/sergi/go-diff/diffmatchpatch"
)

// syntaxStyle is read for its token foreground colours only; the diff
// backgrounds come from the TUI theme.
const syntaxStyle = "nord"

type LineKind int

const (
	KindContext LineKind = iota
	KindAdd
	KindDelete
)

type Seg struct {
	Text     string
	Emphasis bool   // characters that changed relative to the paired line
	Color    string // "#rrggbb" foreground, or "" for the terminal default
}

type Line struct {
	Kind   LineKind
	OldNum int // 0 when the line does not exist on the old side
	NewNum int // 0 when the line does not exist on the new side
	Segs   []Seg
}

type rawLine struct {
	kind    LineKind
	content string
}

// Diff computes a rich line diff between oldCode and newCode, numbering both
// sides from startLine and colouring each line for the given chroma lexer name.
// Replaced line pairs carry word-level emphasis.
func Diff(oldCode, newCode string, startLine int, language string) []Line {
	flat := lineDiff(oldCode, newCode)

	var out []Line
	oldN, newN := startLine, startLine
	for i := 0; i < len(flat); {
		switch flat[i].kind {
		case KindContext:
			out = append(out, Line{Kind: KindContext, OldNum: oldN, NewNum: newN, Segs: []Seg{{Text: flat[i].content}}})
			oldN++
			newN++
			i++
		case KindDelete:
			start := i
			for i < len(flat) && flat[i].kind == KindDelete {
				i++
			}
			dels := flat[start:i]
			istart := i
			for i < len(flat) && flat[i].kind == KindAdd {
				i++
			}
			adds := flat[istart:i]
			emitReplace(&out, dels, adds, &oldN, &newN)
		default: // KindAdd with no preceding delete
			out = append(out, Line{Kind: KindAdd, NewNum: newN, Segs: []Seg{{Text: flat[i].content}}})
			newN++
			i++
		}
	}

	for i := range out {
		out[i].Segs = applySyntax(out[i].Segs, language)
	}
	return out
}

// lineDiff classifies every line of the snippets with a line-mode
// diff-match-patch run. Unlike a unified-diff windowing, it elides nothing, so
// the sequential line numbering in Diff stays correct even when two edits are
// far apart in the excerpt.
func lineDiff(oldCode, newCode string) []rawLine {
	dmp := diffmatchpatch.New()
	a, b, lineArray := dmp.DiffLinesToChars(ensureTrailingNewline(oldCode), ensureTrailingNewline(newCode))
	diffs := dmp.DiffCharsToLines(dmp.DiffMain(a, b, false), lineArray)

	var flat []rawLine
	for _, d := range diffs {
		kind := KindContext
		switch d.Type {
		case diffmatchpatch.DiffInsert:
			kind = KindAdd
		case diffmatchpatch.DiffDelete:
			kind = KindDelete
		}
		for _, line := range splitLines(d.Text) {
			flat = append(flat, rawLine{kind: kind, content: line})
		}
	}
	return flat
}

func splitLines(block string) []string {
	if block == "" {
		return nil
	}
	lines := strings.Split(block, "\n")
	if lines[len(lines)-1] == "" { // drop the empty tail left by the final newline
		lines = lines[:len(lines)-1]
	}
	return lines
}

func emitReplace(out *[]Line, dels, adds []rawLine, oldN, newN *int) {
	if len(dels) == len(adds) && len(dels) > 0 {
		delSegs := make([][]Seg, len(dels))
		addSegs := make([][]Seg, len(adds))
		for k := range dels {
			delSegs[k], addSegs[k] = wordSegs(dels[k].content, adds[k].content)
		}
		for k := range dels {
			*out = append(*out, Line{Kind: KindDelete, OldNum: *oldN, Segs: delSegs[k]})
			*oldN++
		}
		for k := range adds {
			*out = append(*out, Line{Kind: KindAdd, NewNum: *newN, Segs: addSegs[k]})
			*newN++
		}
		return
	}
	for _, d := range dels {
		*out = append(*out, Line{Kind: KindDelete, OldNum: *oldN, Segs: []Seg{{Text: d.content}}})
		*oldN++
	}
	for _, a := range adds {
		*out = append(*out, Line{Kind: KindAdd, NewNum: *newN, Segs: []Seg{{Text: a.content}}})
		*newN++
	}
}

// wordSegs emphasises the characters that differ between a deleted line and its
// paired added line.
func wordSegs(del, add string) (delSegs, addSegs []Seg) {
	dmp := diffmatchpatch.New()
	diffs := dmp.DiffCleanupSemantic(dmp.DiffMain(del, add, false))
	for _, d := range diffs {
		switch d.Type {
		case diffmatchpatch.DiffEqual:
			delSegs = append(delSegs, Seg{Text: d.Text})
			addSegs = append(addSegs, Seg{Text: d.Text})
		case diffmatchpatch.DiffDelete:
			delSegs = append(delSegs, Seg{Text: d.Text, Emphasis: true})
		case diffmatchpatch.DiffInsert:
			addSegs = append(addSegs, Seg{Text: d.Text, Emphasis: true})
		}
	}
	return delSegs, addSegs
}

// applySyntax re-splits the line's segments so each carries a single
// (emphasis, syntax colour) pair.
func applySyntax(segs []Seg, language string) []Seg {
	var full strings.Builder
	for _, s := range segs {
		full.WriteString(s.Text)
	}
	runes := []rune(full.String())
	if len(runes) == 0 {
		return segs
	}

	emph := make([]bool, len(runes))
	pos := 0
	for _, s := range segs {
		for range s.Text {
			emph[pos] = s.Emphasis
			pos++
		}
	}
	colors := syntaxColours(full.String(), len(runes), language)

	var out []Seg
	for i := 0; i < len(runes); {
		j := i + 1
		for j < len(runes) && emph[j] == emph[i] && colors[j] == colors[i] {
			j++
		}
		out = append(out, Seg{Text: string(runes[i:j]), Emphasis: emph[i], Color: colors[i]})
		i = j
	}
	return out
}

// syntaxColours returns a per-rune slice of "#rrggbb" colours. It tokenises with
// chroma's lexer and reads the style's colours directly rather than using a
// chroma formatter — the formatter emits SGR resets that would clear the diff
// background mid-line.
func syntaxColours(text string, n int, language string) []string {
	colors := make([]string, n)
	lexer := lexers.Get(language)
	if lexer == nil {
		return colors
	}
	iterator, err := lexer.Tokenise(nil, text)
	if err != nil {
		return colors
	}
	style := styles.Get(syntaxStyle)
	pos := 0
	for token := iterator(); token != chroma.EOF; token = iterator() {
		hex := ""
		if entry := style.Get(token.Type); entry.Colour.IsSet() {
			hex = entry.Colour.String()
		}
		for range token.Value {
			if pos < n {
				colors[pos] = hex
			}
			pos++
		}
	}
	return colors
}

func ensureTrailingNewline(s string) string {
	if s == "" || strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}
