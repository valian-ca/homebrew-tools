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
		{"hook:assistant-turn", true},
		{"forge:campaign-saved", true},
		{"forge:outcome-recorded", true},
		{"forge:pass", true},
		{"forge:report-linked", true},
		{"forge:run-start", true},
		{"forge:testplan-linked", true},
		{"forge:testplan-published", true},
		{"forge:wave-close", true},
		{"forge:wave-open", true},
		{"skill:phase-start", true},
		{"skill:phase-end", true},
		{"skill:ticket-created", true},
		{"skill:activity", true},
		{"skill:ship-complete", true},
		{"transcript:ai-title", true},
		{"transcript:custom-title", true},
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

func TestAllReturnsTwentyThreeTypes(t *testing.T) {
	if got := len(All()); got != 23 {
		t.Errorf("All() returned %d types, want 23", got)
	}
}

func TestParsePayload(t *testing.T) {
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

func TestParseJSONPayload(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    map[string]any
		wantErr bool
	}{
		{
			name: "nested object with numeric values",
			args: []string{`usage={"input_tokens":1200,"cache_creation":{"ephemeral_5m_input_tokens":800}}`},
			want: map[string]any{"usage": map[string]any{
				"input_tokens":   float64(1200),
				"cache_creation": map[string]any{"ephemeral_5m_input_tokens": float64(800)},
			}},
		},
		{
			name: "scalar number",
			args: []string{"count=42"},
			want: map[string]any{"count": float64(42)},
		},
		{
			name: "scalar boolean",
			args: []string{"success=true"},
			want: map[string]any{"success": true},
		},
		{
			name: "quoted string",
			args: []string{`title="2025"`},
			want: map[string]any{"title": "2025"},
		},
		{
			name: "null value",
			args: []string{"cost=null"},
			want: map[string]any{"cost": nil},
		},
		{
			name: "array value",
			args: []string{"models=[\"a\",\"b\"]"},
			want: map[string]any{"models": []any{"a", "b"}},
		},
		{
			name: "value containing equals",
			args: []string{`q="a=b"`},
			want: map[string]any{"q": "a=b"},
		},
		{
			name:    "bare word is not JSON",
			args:    []string{"title=hello"},
			wantErr: true,
		},
		{
			name:    "truncated object",
			args:    []string{`usage={"input_tokens":`},
			wantErr: true,
		},
		{
			name:    "empty value",
			args:    []string{"usage="},
			wantErr: true,
		},
		{
			name:    "no equals",
			args:    []string{"usage"},
			wantErr: true,
		},
		{
			name:    "empty key",
			args:    []string{"={}"},
			wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseJSONPayload(c.args)
			if (err != nil) != c.wantErr {
				t.Fatalf("ParseJSONPayload(%v) error = %v, wantErr = %v", c.args, err, c.wantErr)
			}
			if c.wantErr {
				return
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("ParseJSONPayload(%v) = %v, want %v", c.args, got, c.want)
			}
		})
	}
}
