package highlight

import "testing"

func kinds(lines []Line) map[LineKind]int {
	m := map[LineKind]int{}
	for _, l := range lines {
		m[l.Kind]++
	}
	return m
}

func TestDiffPureAddition(t *testing.T) {
	lines := Diff("", "new one\nnew two", 10, "")
	k := kinds(lines)
	if k[KindDelete] != 0 || k[KindContext] != 0 {
		t.Fatalf("empty old should yield only additions, got %v", k)
	}
	if k[KindAdd] != 2 {
		t.Fatalf("expected 2 additions, got %d", k[KindAdd])
	}
	if lines[0].NewNum != 10 || lines[1].NewNum != 11 {
		t.Fatalf("additions should be numbered 10,11 on the new side, got %d,%d", lines[0].NewNum, lines[1].NewNum)
	}
	if lines[0].OldNum != 0 {
		t.Fatalf("additions have no old line number, got %d", lines[0].OldNum)
	}
}

func TestDiffPureDeletion(t *testing.T) {
	lines := Diff("old one\nold two", "", 5, "")
	k := kinds(lines)
	if k[KindAdd] != 0 || k[KindContext] != 0 {
		t.Fatalf("empty new should yield only deletions, got %v", k)
	}
	if lines[0].OldNum != 5 || lines[1].OldNum != 6 {
		t.Fatalf("deletions should be numbered 5,6 on the old side, got %d,%d", lines[0].OldNum, lines[1].OldNum)
	}
}

func TestDiffContextNumbering(t *testing.T) {
	old := "const a = parse(req)\n// changed from foo to bar\nconst r = bar(a)"
	neu := "const a = parse(req)\nconst r = bar(a)"
	lines := Diff(old, neu, 40, "")
	if kinds(lines)[KindDelete] != 1 {
		t.Fatalf("expected exactly 1 deletion (the comment line), got %v", kinds(lines))
	}
	if lines[0].Kind != KindContext || lines[0].OldNum != 40 || lines[0].NewNum != 40 {
		t.Fatalf("first context line should be numbered 40/40, got kind=%d old=%d new=%d", lines[0].Kind, lines[0].OldNum, lines[0].NewNum)
	}
}

func TestWordLevelEmphasis(t *testing.T) {
	lines := Diff("const a = 1", "const a = 2", 1, "")
	if len(lines) != 2 {
		t.Fatalf("a single replaced line should yield 1 delete + 1 add, got %d lines", len(lines))
	}
	del, add := lines[0], lines[1]
	if del.Kind != KindDelete || add.Kind != KindAdd {
		t.Fatalf("expected delete then add, got %d,%d", del.Kind, add.Kind)
	}
	if !hasEmphasis(del.Segs) || !hasEmphasis(add.Segs) {
		t.Fatalf("replaced lines should carry word-level emphasis: del=%+v add=%+v", del.Segs, add.Segs)
	}
	// The shared prefix must NOT be emphasised.
	if del.Segs[0].Emphasis || del.Segs[0].Text != "const a = " {
		t.Fatalf("shared prefix should be a non-emphasised segment, got %+v", del.Segs[0])
	}
}

func TestSyntaxColoursApplied(t *testing.T) {
	// A known Go keyword should pick up a non-empty foreground colour.
	lines := Diff("", "package main", 1, "go")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	colored := false
	for _, s := range lines[0].Segs {
		if s.Color != "" {
			colored = true
		}
	}
	if !colored {
		t.Fatalf("expected at least one syntax-coloured segment for Go, got %+v", lines[0].Segs)
	}
}

func TestSyntaxColoursAbsentForUnknownLanguage(t *testing.T) {
	lines := Diff("", "whatever tokens here", 1, "no-such-lexer")
	for _, s := range lines[0].Segs {
		if s.Color != "" {
			t.Fatalf("unknown language must not colour segments, got %+v", s)
		}
	}
}

func TestDiffUnequalRuns(t *testing.T) {
	lines := Diff("a\nb\nc", "x", 1, "")
	k := kinds(lines)
	if k[KindDelete] != 3 || k[KindAdd] != 1 {
		t.Fatalf("expected 3 deletes + 1 add, got %v", k)
	}
	for _, l := range lines {
		if hasEmphasis(l.Segs) {
			t.Fatalf("unequal-run lines must not carry word emphasis: %+v", l.Segs)
		}
	}
}

func TestApplySyntaxMultibyteRoundTrip(t *testing.T) {
	const line = "// café ☕ value"
	lines := Diff("", line, 1, "go")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	var joined string
	for _, s := range lines[0].Segs {
		joined += s.Text
	}
	if joined != line {
		t.Fatalf("segment join must reassemble the original line, got %q", joined)
	}
}

func hasEmphasis(segs []Seg) bool {
	for _, s := range segs {
		if s.Emphasis {
			return true
		}
	}
	return false
}
