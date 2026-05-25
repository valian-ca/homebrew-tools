package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

func fix() *Action { a := ActionFix; return &a }

func TestParseValidInput(t *testing.T) {
	data := []byte(`{
		"schemaVersion": 1,
		"branch": "feat/x",
		"mergeBase": "abc123",
		"findings": [
			{"id": 1, "title": "t", "group": "comments", "score": 90,
			 "codeExcerpt": "a := 1", "proposedFix": {"code": "a := 2"}, "selection": "fix"}
		]
	}`)
	in, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if in.SchemaVersion != 1 || len(in.Findings) != 1 {
		t.Fatalf("unexpected parse: %+v", in)
	}
	if in.Findings[0].Selection == nil || *in.Findings[0].Selection != ActionFix {
		t.Fatalf("selection not parsed: %+v", in.Findings[0].Selection)
	}
}

func TestParseSchemaMismatch(t *testing.T) {
	_, err := Parse([]byte(`{"schemaVersion": 2, "findings": []}`))
	if err == nil || !strings.Contains(err.Error(), "schemaVersion") {
		t.Fatalf("expected schemaVersion error, got %v", err)
	}
}

func TestParseMalformed(t *testing.T) {
	_, err := Parse([]byte(`{not json`))
	if err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("expected malformed error, got %v", err)
	}
}

func TestParseEmptyFindings(t *testing.T) {
	in, err := Parse([]byte(`{"schemaVersion": 1, "findings": []}`))
	if err != nil {
		t.Fatalf("empty findings should be valid: %v", err)
	}
	if len(in.Findings) != 0 {
		t.Fatalf("expected zero findings, got %d", len(in.Findings))
	}
}

func TestValidateBothSidesEmpty(t *testing.T) {
	in := &Input{SchemaVersion: 1, Findings: []Finding{{ID: 7}}}
	if err := in.Validate(); err == nil || !strings.Contains(err.Error(), "finding 7") {
		t.Fatalf("expected both-empty rejection, got %v", err)
	}
}

func TestValidateOneSidePopulated(t *testing.T) {
	cases := map[string]Finding{
		"only excerpt":  {ID: 1, CodeExcerpt: "x"},
		"only proposed": {ID: 2, ProposedFix: ProposedFix{Code: "y"}},
	}
	for name, f := range cases {
		t.Run(name, func(t *testing.T) {
			in := &Input{SchemaVersion: 1, Findings: []Finding{f}}
			if err := in.Validate(); err != nil {
				t.Fatalf("expected valid, got %v", err)
			}
		})
	}
}

func TestValidateBadSelection(t *testing.T) {
	bad := Action("maybe")
	in := &Input{SchemaVersion: 1, Findings: []Finding{{ID: 3, CodeExcerpt: "x", Selection: &bad}}}
	if err := in.Validate(); err == nil || !strings.Contains(err.Error(), "selection") {
		t.Fatalf("expected selection error, got %v", err)
	}
}

func TestOutputDiscussPromptPresence(t *testing.T) {
	empty := ""
	out := Output{
		SchemaVersion: 1,
		Decisions: []Decision{
			{ID: 1, Action: ActionFix},
			{ID: 2, Action: ActionDiscuss, DiscussPrompt: &empty},
		},
	}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	// fix decision: no discussPrompt key at all.
	if strings.Contains(s, `"id":1`) && strings.Contains(s, `"discussPrompt"`) &&
		strings.Index(s, `"discussPrompt"`) < strings.Index(s, `"id":2`) {
		t.Fatalf("fix decision should not carry discussPrompt: %s", s)
	}
	// discuss decision: discussPrompt present even when empty.
	var round Output
	if err := json.Unmarshal(data, &round); err != nil {
		t.Fatal(err)
	}
	if round.Decisions[1].DiscussPrompt == nil {
		t.Fatalf("empty discuss prompt must round-trip as present, got nil")
	}
}

func TestActionValid(t *testing.T) {
	for _, a := range []Action{ActionFix, ActionSkip, ActionDiscuss} {
		if !a.Valid() {
			t.Fatalf("%q should be valid", a)
		}
	}
	if Action("nope").Valid() {
		t.Fatalf("unknown action should be invalid")
	}
	_ = fix()
}
