// Package transcript parses Claude Code transcript JSONL files and derives
// atelier events from them. The format Anthropic writes to ~/.claude/projects/
// is not a stable public contract — leaves and nesting may drift between
// releases. All format knowledge lives in this package so a future Anthropic
// change patches a single module.
//
// Event derivation contract (matches what the bash hooks emitted before VAL-201):
//
//   - assistant record (content[tool_use])         → hook:pre-tool-use
//   - assistant record (any content, dedup msg_id) → hook:assistant-turn
//   - user record (content[tool_result])           → hook:post-tool-use
//   - user record (no tool_result block, fresh     → hook:user-prompt-submit
//     promptId)
//
// The post-tool-use discriminator is the presence of a tool_result content
// block on message.content; tool_use_id and is_error live on that block. The
// top-level toolUseResult field is a per-tool wrapper of varying shape
// (stdout/stderr for Bash, filenames for Glob, …) and on Claude Code 2.x
// never carries tool_use_id. The legacy top-level shape is still consulted
// as a fallback so pre-2.x fixtures keep working.
//
// Stop and SessionEnd are NOT derived here — the bash hooks at the plugin
// level still emit those (cf. VAL-201 plan). The JSONL exposes no reliable
// end-of-turn or end-of-session marker, and tokens-per-phase doesn't need them.
package transcript

import "encoding/json"

// Record is the minimal shape we need to read from each JSONL line. Fields we
// don't consume are dropped during Unmarshal — the parser is intentionally
// permissive so unknown record types pass through without error.
//
// `Message.Usage` and `Message.Model` are typed as json.RawMessage so unknown
// nested fields (e.g. a future `usage.custom_token_class`) flow through
// byte-identical into the emitted hook:assistant-turn payload.
type Record struct {
	Type          string         `json:"type"`
	Message       Message        `json:"message,omitempty"`
	PromptID      string         `json:"promptId,omitempty"`
	ToolUseResult *ToolUseResult `json:"toolUseResult,omitempty"`
	IsMeta        bool           `json:"isMeta,omitempty"`
	AiTitle       string         `json:"aiTitle,omitempty"`
	CustomTitle   string         `json:"customTitle,omitempty"`
}

type Message struct {
	ID      string          `json:"id,omitempty"`
	Role    string          `json:"role,omitempty"`
	Model   json.RawMessage `json:"model,omitempty"`
	Usage   json.RawMessage `json:"usage,omitempty"`
	Content json.RawMessage `json:"content,omitempty"`
}

// `Type` discriminates: "thinking" / "text" (ignored), "tool_use" (assistant
// message → hook:pre-tool-use), "tool_result" (user message → hook:post-tool-use).
// `ID` / `Name` / `Input` are only meaningful when Type == "tool_use";
// `ToolUseID` / `IsError` only when Type == "tool_result".
type ContentBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// Only ToolUseID and IsError affect derivation; the rest of the wrapper is
// intentionally not modelled.
type ToolUseResult struct {
	ToolUseID string `json:"tool_use_id,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

// ToolInput captures the subset of a tool_use input object that the existing
// hook:pre-tool-use payload contract reads. Anything else is ignored — the
// projection trigger (cf. categorize-tool.ts in valian-dashboards) operates
// on these eight fields exclusively.
type ToolInput struct {
	FilePath    string `json:"file_path,omitempty"`
	Command     string `json:"command,omitempty"`
	Pattern     string `json:"pattern,omitempty"`
	Query       string `json:"query,omitempty"`
	URL         string `json:"url,omitempty"`
	Description string `json:"description,omitempty"`
	Skill       string `json:"skill,omitempty"`
}
