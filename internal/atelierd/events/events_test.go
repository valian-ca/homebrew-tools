package events

import (
	"reflect"
	"testing"
)

func TestIsValid(t *testing.T) {
	cases := []struct {
		s    string
		want bool
	}{
		{"hook:session-start", true},
		{"hook:user-prompt-submit", true},
		{"hook:pre-tool-use", true},
		{"hook:post-tool-use", true},
		{"hook:stop", true},
		{"hook:session-end", true},
		{"skill:phase-start", true},
		{"skill:phase-end", true},
		{"skill:ticket-created", true},
		{"skill:activity", true},
		{"skill:ship-complete", true},
		{"hook:invented", false},
		{"", false},
		{"skill:phase-START", false},
		{"hook:session-start ", false},
	}
	for _, c := range cases {
		if got := IsValid(c.s); got != c.want {
			t.Errorf("IsValid(%q) = %v, want %v", c.s, got, c.want)
		}
	}
}

func TestAllReturnsElevenTypes(t *testing.T) {
	if got := len(All()); got != 11 {
		t.Errorf("All() returned %d types, want 11", got)
	}
}

func TestParsePayload(t *testing.T) {
	// VAL-195: every value is stored verbatim as a string. Type coercion is
	// the consumer's responsibility (Zod schemas in valian-dashboards).
	cases := []struct {
		name    string
		args    []string
		want    map[string]any
		wantErr bool
	}{
		{
			name: "string value",
			args: []string{"foo=bar"},
			want: map[string]any{"foo": "bar"},
		},
		{
			name: "numeric-looking value stays a string",
			args: []string{"count=42"},
			want: map[string]any{"count": "42"},
		},
		{
			name: "float-looking value stays a string",
			args: []string{"rate=3.14"},
			want: map[string]any{"rate": "3.14"},
		},
		{
			name: "boolean-looking true stays a string",
			args: []string{"success=true"},
			want: map[string]any{"success": "true"},
		},
		{
			name: "boolean-looking false stays a string",
			args: []string{"success=false"},
			want: map[string]any{"success": "false"},
		},
		{
			name: "case is preserved (no lowercasing)",
			args: []string{"a=TRUE", "b=False"},
			want: map[string]any{"a": "TRUE", "b": "False"},
		},
		{
			name: "empty string value",
			args: []string{"foo="},
			want: map[string]any{"foo": ""},
		},
		{
			name: "value containing equals",
			args: []string{"q=a=b=c"},
			want: map[string]any{"q": "a=b=c"},
		},
		{
			name: "mixed",
			args: []string{"tool=Edit", "filesEdited=3", "success=true"},
			want: map[string]any{"tool": "Edit", "filesEdited": "3", "success": "true"},
		},
		{
			name: "title with mono-token numeric value (VAL-195 regression case)",
			args: []string{"title=2025"},
			want: map[string]any{"title": "2025"},
		},
		{
			name:    "no equals",
			args:    []string{"foo"},
			wantErr: true,
		},
		{
			name:    "empty key",
			args:    []string{"=foo"},
			wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParsePayload(c.args)
			if (err != nil) != c.wantErr {
				t.Fatalf("ParsePayload(%v) error = %v, wantErr = %v", c.args, err, c.wantErr)
			}
			if c.wantErr {
				return
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("ParsePayload(%v) = %v, want %v", c.args, got, c.want)
			}
		})
	}
}
