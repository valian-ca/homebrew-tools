package highlight

import (
	"strings"
	"testing"
)

func kinds(lines []Line) map[LineKind]int {
	m := map[LineKind]int{}
	for _, l := range lines {
		m[l.Kind]++
	}
	return m
}

func TestDiffPureAddition(t *testing.T) {
	lines := Diff("", "new line one\nnew line two")
	k := kinds(lines)
	if k[KindDelete] != 0 || k[KindContext] != 0 {
		t.Fatalf("empty old should yield only additions, got %v", k)
	}
	if k[KindAdd] != 2 {
		t.Fatalf("expected 2 additions, got %d", k[KindAdd])
	}
}

func TestDiffPureDeletion(t *testing.T) {
	lines := Diff("old line one\nold line two", "")
	k := kinds(lines)
	if k[KindAdd] != 0 || k[KindContext] != 0 {
		t.Fatalf("empty new should yield only deletions, got %v", k)
	}
	if k[KindDelete] != 2 {
		t.Fatalf("expected 2 deletions, got %d", k[KindDelete])
	}
}

func TestDiffBothPopulated(t *testing.T) {
	old := "const a = parse(req)\n// changed from foo to bar\nconst r = bar(a)"
	neu := "const a = parse(req)\nconst r = bar(a)"
	lines := Diff(old, neu)
	k := kinds(lines)
	if k[KindDelete] != 1 {
		t.Fatalf("expected exactly 1 deletion (the comment line), got %d (%v)", k[KindDelete], k)
	}
	if k[KindContext] < 1 {
		t.Fatalf("expected context lines around the change, got %v", k)
	}
}

func TestCodeNoColorIsPlain(t *testing.T) {
	src := "const a = 1"
	got := Code(src, "typescript", true)
	if got != src {
		t.Fatalf("NO_COLOR must return src unchanged, got %q", got)
	}
}

func TestCodeUnknownLanguageIsPlain(t *testing.T) {
	src := "weird ::: tokens"
	got := Code(src, "no-such-lexer", false)
	if got != src {
		t.Fatalf("unknown lexer must return src unchanged, got %q", got)
	}
}

func TestCodeHighlightsKnownLanguage(t *testing.T) {
	got := Code("package main", "go", false)
	if !strings.Contains(got, "\x1b[") {
		t.Fatalf("expected ANSI escapes for a known lexer, got %q", got)
	}
}
