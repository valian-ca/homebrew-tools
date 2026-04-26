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
			name: "int value",
			args: []string{"count=42"},
			want: map[string]any{"count": int64(42)},
		},
		{
			name: "float value",
			args: []string{"rate=3.14"},
			want: map[string]any{"rate": 3.14},
		},
		{
			name: "bool true",
			args: []string{"success=true"},
			want: map[string]any{"success": true},
		},
		{
			name: "bool false",
			args: []string{"success=false"},
			want: map[string]any{"success": false},
		},
		{
			name: "bool case-insensitive",
			args: []string{"a=TRUE", "b=False"},
			want: map[string]any{"a": true, "b": false},
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
			want: map[string]any{"tool": "Edit", "filesEdited": int64(3), "success": true},
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
