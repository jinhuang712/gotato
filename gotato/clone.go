package gotato

import (
	"maps"
	"slices"
)

func cloneMessages(messages []Message) []Message {
	out := make([]Message, len(messages))
	for i, message := range messages {
		out[i] = message.Clone()
	}
	return out
}

func cloneParts(content []ContentPart) []ContentPart {
	out := make([]ContentPart, len(content))
	for i, part := range content {
		out[i] = part
		out[i].Data = slices.Clone(part.Data)
		out[i].Signature = slices.Clone(part.Signature)
		out[i].Metadata = maps.Clone(part.Metadata)
	}
	return out
}

func cloneToolResultPtr(result ToolResult) *ToolResult { clone := result.Clone(); return &clone }

func cloneToolSpec(spec ToolSpec) ToolSpec {
	out := spec
	out.InputSchema = slices.Clone(spec.InputSchema)
	out.OutputSchema = slices.Clone(spec.OutputSchema)
	out.Metadata = maps.Clone(spec.Metadata)
	return out
}

func cloneToolSpecs(specs []ToolSpec) []ToolSpec {
	out := make([]ToolSpec, len(specs))
	for i, spec := range specs {
		out[i] = cloneToolSpec(spec)
	}
	return out
}

func cloneToolCalls(calls []ToolCall) []ToolCall {
	if calls == nil {
		return nil
	}
	out := make([]ToolCall, len(calls))
	for i, call := range calls {
		out[i] = call
		out[i].Arguments = append([]byte(nil), call.Arguments...)
		out[i].Signature = slices.Clone(call.Signature)
	}
	return out
}
