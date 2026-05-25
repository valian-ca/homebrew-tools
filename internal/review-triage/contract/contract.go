package contract

import (
	"encoding/json"
	"fmt"
	"os"
)

// SchemaVersion is the only schema this binary understands; a mismatch is a
// hard error (exit 2) so the skill can evolve the schema without an older
// binary silently misreading its input.
const SchemaVersion = 1

type Action string

const (
	ActionFix     Action = "fix"
	ActionSkip    Action = "skip"
	ActionDiscuss Action = "discuss"
)

func (a Action) Valid() bool {
	return a == ActionFix || a == ActionSkip || a == ActionDiscuss
}

type ProposedFix struct {
	Explanation string `json:"explanation"`
	Code        string `json:"code"`
}

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
	// Selection is the suggested starting action; nil means the upstream had no
	// opinion and the user must choose.
	Selection *Action `json:"selection"`
}

type Input struct {
	SchemaVersion int       `json:"schemaVersion"`
	Branch        string    `json:"branch"`
	MergeBase     string    `json:"mergeBase"`
	Findings      []Finding `json:"findings"`
}

type Decision struct {
	ID     int    `json:"id"`
	Action Action `json:"action"`
	// DiscussPrompt is present iff Action == ActionDiscuss and may be the empty
	// string, so it is a pointer — omitempty on a plain string cannot tell ""
	// from absent.
	DiscussPrompt *string `json:"discussPrompt,omitempty"`
}

type Output struct {
	SchemaVersion int        `json:"schemaVersion"`
	Decisions     []Decision `json:"decisions"`
}

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

func Load(path string) (*Input, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Validate enforces the invariants the struct tags cannot express: the schema
// version, the per-finding "at least one side populated" rule, and selection
// well-formedness.
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

// Write serializes the output to path atomically (temp file + rename) so a
// crash mid-write can't truncate a previously written decisions file.
func (o Output) Write(path string) error {
	data, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
