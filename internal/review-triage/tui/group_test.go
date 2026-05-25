package tui

import "testing"

func TestScoreBucket(t *testing.T) {
	cases := []struct {
		score int
		want  string
	}{
		{101, "100"}, {100, "100"},
		{99, "<100"}, {76, "<100"},
		{75, "≤75"}, {51, "≤75"},
		{50, "≤50"}, {26, "≤50"},
		{25, "≤25"}, {1, "≤25"},
		{0, "0"}, {-3, "0"},
	}
	for _, c := range cases {
		if got := scoreBucket(c.score); got != c.want {
			t.Fatalf("scoreBucket(%d) = %q, want %q", c.score, got, c.want)
		}
	}
}
