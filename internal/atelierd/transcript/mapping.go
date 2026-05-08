package transcript

import "encoding/json"

// preToolUsePayload mirrors the hook:pre-tool-use payload contract that
// emit-pre-tool-use.sh produced before VAL-201. The projection trigger
// (categorize-tool.ts in valian-dashboards) reads exactly these fields; any
// drift here is a contract break.
//
// Empty fields are omitted — the bash hook only forwarded non-empty values.
// The map is constructed key-by-key for that reason rather than via a struct
// with omitempty (the verbatim-string convention in events.ParsePayload makes
// any zero-value distinguishable from "0"/"false" and we want symmetry with
// what the bash hook would have emitted).
func preToolUsePayload(name string, rawInput json.RawMessage) map[string]any {
	payload := map[string]any{"tool": name}
	if len(rawInput) == 0 {
		return payload
	}
	var in ToolInput
	if err := json.Unmarshal(rawInput, &in); err != nil {
		return payload
	}
	if in.FilePath != "" {
		payload["filePath"] = in.FilePath
	}
	if in.Command != "" {
		payload["command"] = in.Command
	}
	if in.Pattern != "" {
		payload["pattern"] = in.Pattern
	}
	if in.Query != "" {
		payload["query"] = in.Query
	}
	if in.URL != "" {
		payload["url"] = in.URL
	}
	if in.Description != "" {
		payload["description"] = in.Description
	}
	// skillName is the project's contract for the dashboard's per-skill
	// aggregation (categorize-tool.ts only consumes it when tool === "Skill").
	// Forwarding it from any tool that happens to carry an "skill" key in its
	// input would contaminate the groupBy with whatever value Claude passed.
	if name == "Skill" && in.Skill != "" {
		payload["skillName"] = in.Skill
	}
	return payload
}

// postToolUsePayload mirrors emit-post-tool-use.sh: forward the tool name and
// a verbatim "true"/"false" string for success. Per VAL-195 the value goes
// over the wire as a string; the EventZod consumer accepts both legacy
// boolean and string forms.
func postToolUsePayload(name string, isError bool) map[string]any {
	success := "true"
	if isError {
		success = "false"
	}
	return map[string]any{"tool": name, "success": success}
}

// assistantTurnPayload returns the {usage, model} payload, preserving the
// exact bytes of usage and model from the JSONL line. Per VAL-201 AC 3, this
// must be byte-for-byte equal to .message.usage and .message.model. Per
// AC 5, an unknown nested field added by Anthropic flows through unchanged.
//
// usage is forwarded as json.RawMessage so the outbox JSON encoder writes the
// original bytes back out without re-marshalling. model is decoded to a
// string when it's a JSON string (the common case); when it's a non-string
// JSON value (unlikely but defensive) we forward the raw bytes the same way
// as usage.
func assistantTurnPayload(rawUsage, rawModel json.RawMessage) map[string]any {
	out := map[string]any{}
	if len(rawUsage) > 0 {
		out["usage"] = rawUsage
	}
	if len(rawModel) > 0 {
		var s string
		if err := json.Unmarshal(rawModel, &s); err == nil {
			out["model"] = s
		} else {
			out["model"] = rawModel
		}
	}
	return out
}
