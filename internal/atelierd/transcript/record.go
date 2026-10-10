// Package transcript reads Claude Code transcript JSONL files and derives
// atelier events from them. The format Anthropic writes to ~/.claude/projects/
// is not a stable public contract — leaves and nesting may drift between
// releases. All format knowledge lives in this package so a future Anthropic
// change patches a single module.
//
// Event derivation contract:
//
//   - any line with a parseable `timestamp`     → activity:minute, at most once
//     per session and UTC minute of that timestamp (never the read time)
//   - ai-title / custom-title line              → transcript:ai-title /
//     transcript:custom-title, on a real change of (title, kind) only
//
// A session tree is the parent transcript plus every agent-*.jsonl at any
// depth under <parent-without-ext>/subagents/ (Task subagents and Workflow
// agents). Every heartbeat carries the parent's claudeSessionId.
package transcript

// Record is the minimal shape read from each JSONL line. Unknown fields are
// dropped during Unmarshal so unknown record types pass through without error.
type Record struct {
	Type        string `json:"type"`
	Timestamp   string `json:"timestamp,omitempty"`
	AiTitle     string `json:"aiTitle,omitempty"`
	CustomTitle string `json:"customTitle,omitempty"`
}
