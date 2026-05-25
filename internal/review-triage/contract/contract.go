// Package contract holds the Go types mirroring the review-triage JSON
// schema and the parse/validate/write helpers that bound the binary's I/O.
package contract

import (
	"encoding/json"
	"fmt"
	"os"
)

// SchemaVersion is the only input/output schema this binary understands.
// A mismatch is a hard error (exit 2) so the skill can evolve the schema
// without silently misreading an older binary's output.
const SchemaVersion = 1

// Action is the decision retained for a finding (output side).
type Action string

const (
	ActionFix     Action = "fix"
	ActionSkip    Action = "skip"
	ActionDiscuss Action = "discuss"
)

// Valid reports whether a is one of the three known actions.
func (a Action) Valid() bool {
	return a == ActionFix || a == ActionSkip || a == ActionDiscuss
}

// ProposedFix is the suggested replacement for a finding's code.
type ProposedFix struct {
	Explanation string `json:"explanation"`
	Code        string `json:"code"`
}

// Finding is one review issue presented for triage. Provided in the input,
// never mutated by the binary.
type Finding struct {
	ID          int         `json:"id"`
	Title       string      `json:"title"`
	Group       string      `json:"group"`
	AgentLabel  string      `json:"agentLabel"`
	Score       int         `json:"score"`
	Explanation string      `json:"explanation"`
	File        string      `json:"file"`
	LineStart   int         `json:"lineStart"`
	LineEnd     int         `json:"lineEnd"`
	Language    string      `json:"language"`
	CodeExcerpt string      `json:"codeExcerpt"`
	ProposedFix ProposedFix `json:"proposedFix"`
	// Selection is the suggested starting action. A nil pointer means the
	// upstream had no opinion and the user must choose.
	Selection *Action `json:"selection"`
}

// Input is the document the skill writes for the binary to read.
type Input struct {
	SchemaVersion int       `json:"schemaVersion"`
	Branch        string    `json:"branch"`
	MergeBase     string    `json:"mergeBase"`
	Findings      []Finding `json:"findings"`
}

// Decision is the action the user retained for one finding (output side).
type Decision struct {
	ID     int    `json:"id"`
	Action Action `json:"action"`
	// DiscussPrompt is present iff Action == ActionDiscuss. It may be the
	// empty string, so it is a pointer rather than relying on omitempty of
	// a plain string (which cannot distinguish "" from absent).
	DiscussPrompt *string `json:"discussPrompt,omitempty"`
}

// Output is the document the binary writes on a successful submit.
type Output struct {
	SchemaVersion int        `json:"schemaVersion"`
	Decisions     []Decision `json:"decisions"`
}

// Parse decodes and validates an input document.
func Parse(data []byte) (*Input, error) {
	var in Input
	if err := json.Unmarshal(data, &in); err != nil {
		return nil, fmt.Errorf("malformed input JSON: %w", err)
	}
	if err := in.Validate(); err != nil {
		return nil, err
	}
	return &in, nil
}

// Load reads and parses an input document from path.
func Load(path string) (*Input, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Validate enforces the schema invariants that the parser cannot express
// in the struct tags: the schema version, the per-finding "at least one
// side" rule, and selection well-formedness.
func (in *Input) Validate() error {
	if in.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schemaVersion %d (expected %d)", in.SchemaVersion, SchemaVersion)
	}
	for _, f := range in.Findings {
		if f.CodeExcerpt == "" && f.ProposedFix.Code == "" {
			return fmt.Errorf("finding %d has empty codeExcerpt and empty proposedFix.code", f.ID)
		}
		if f.Selection != nil && !f.Selection.Valid() {
			return fmt.Errorf("finding %d has invalid selection %q", f.ID, *f.Selection)
		}
	}
	return nil
}

// Write serializes the output document to path with a trailing newline.
func (o Output) Write(path string) error {
	data, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
