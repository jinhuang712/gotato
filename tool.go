package gotato

import (
	"context"
)

type Tool interface {
	Spec() ToolSpec
	Execute(context.Context, ToolUse, ToolProgress) (ToolResult, error)
}

type ToolSpec struct {
	ID           string            `json:"id"`
	Name         string            `json:"name,omitempty"`
	Description  string            `json:"description,omitempty"`
	InputSchema  []byte            `json:"input_schema,omitempty"`
	OutputSchema []byte            `json:"output_schema,omitempty"`
	Sequential   bool              `json:"sequential,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

type ToolUse struct {
	RunID         RunID       `json:"run_id"`
	Turn          TurnNumber  `json:"turn"`
	CallID        ToolCallID  `json:"call_id"`
	QualifiedID   string      `json:"qualified_id"`
	ArgumentsJSON []byte      `json:"arguments_json"`
	SourceIndex   uint32      `json:"source_index"`
	Executed      bool        `json:"executed"`
	Result        *ToolResult `json:"result,omitempty"`
}

type ToolProgress func(string)
