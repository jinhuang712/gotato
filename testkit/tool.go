package testkit

import (
	"context"
	"slices"
	"sync"

	"github.com/jinhuang712/gotato/gotato"
)

// FakeTool returns a fixed result (or error) and records every ToolUse.
type FakeTool struct {
	mu   sync.Mutex
	spec gotato.ToolSpec
	// Result is returned on each call. Err, when set, is returned instead.
	Result gotato.ToolResult
	Err    error
	// Handler, when set, computes the result per call and overrides Result.
	Handler func(context.Context, gotato.ToolUse) (gotato.ToolResult, error)
	uses    []gotato.ToolUse
}

// NewFakeTool creates a FakeTool with an object schema that accepts any
// properties and returns text.
func NewFakeTool(id, text string) *FakeTool {
	return &FakeTool{
		spec:   gotato.ToolSpec{ID: id, Name: id, Description: "fake tool " + id, InputSchema: []byte(`{"type":"object"}`)},
		Result: gotato.ToolResult{Status: gotato.ToolResultStatusOK, Content: []gotato.ContentPart{{Kind: gotato.ContentKindText, Text: text}}},
	}
}

// WithSchema replaces the input schema.
func (t *FakeTool) WithSchema(schema string) *FakeTool {
	t.spec.InputSchema = []byte(schema)
	return t
}

// Spec implements gotato.Tool.
func (t *FakeTool) Spec() gotato.ToolSpec { return t.spec }

// Execute implements gotato.Tool.
func (t *FakeTool) Execute(ctx context.Context, use gotato.ToolUse, _ gotato.ToolProgressFunc) (gotato.ToolResult, error) {
	t.mu.Lock()
	t.uses = append(t.uses, use)
	handler, err, result := t.Handler, t.Err, t.Result
	t.mu.Unlock()
	if handler != nil {
		return handler(ctx, use)
	}
	if err != nil {
		return gotato.ToolResult{}, err
	}
	return result.Clone(), nil
}

// Uses returns the recorded ToolUses, deep-copied so a caller cannot mutate
// the recording the fake keeps.
func (t *FakeTool) Uses() []gotato.ToolUse {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]gotato.ToolUse, len(t.uses))
	for i, use := range t.uses {
		out[i] = cloneToolUse(use)
	}
	return out
}

func cloneToolUse(use gotato.ToolUse) gotato.ToolUse {
	out := use
	out.Arguments = slices.Clone(use.Arguments)
	return out
}

// Calls returns how many times Execute ran.
func (t *FakeTool) Calls() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.uses)
}
