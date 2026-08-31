package gotato

import (
	"context"
	"io"
)

type Model interface {
	Stream(context.Context, ModelRequest) (ModelStream, error)
}

type ModelRequest struct {
	SystemInstructions string       `json:"system_instructions,omitempty"`
	Messages           []Message    `json:"messages"`
	Tools              []ToolSpec   `json:"tools,omitempty"`
	Options            ModelOptions `json:"options,omitempty"`
}

type ModelOptions struct {
	Temperature      *float64 `json:"temperature,omitempty"`
	MaxTokens        uint32   `json:"max_tokens,omitempty"`
	ReasoningEffort  string   `json:"reasoning_effort,omitempty"`
	ReasoningSummary string   `json:"reasoning_summary,omitempty"`
}

type ModelStream interface {
	Recv(context.Context) (ModelEvent, error)
	Close() error
}

type ModelEventKind string

const (
	ModelTextDelta      ModelEventKind = "text_delta"
	ModelReasoningDelta ModelEventKind = "reasoning_delta"
	// ModelReasoningDone carries an opaque provider reasoning artifact. Core
	// stores it with the reasoning part but never interprets it.
	ModelReasoningDone ModelEventKind = "reasoning_done"
	ModelToolCall      ModelEventKind = "tool_call"
	ModelUsage         ModelEventKind = "usage"
	ModelDone          ModelEventKind = "done"
)

type ModelEvent struct {
	Kind              ModelEventKind `json:"kind"`
	Text              string         `json:"text,omitempty"`
	ReasoningArtifact []byte         `json:"reasoning_artifact,omitempty"`
	ToolCall          *ToolCall      `json:"tool_call,omitempty"`
	Usage             Usage          `json:"usage,omitempty"`
	StopReason        StopReason     `json:"stop_reason,omitempty"`
}

func modelStreamDone() error { return io.EOF }
