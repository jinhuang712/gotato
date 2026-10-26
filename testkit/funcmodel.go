package testkit

import (
	"context"
	"strings"
	"sync"

	gotato "github.com/jinhuang712/gotato/gotato-core"
)

// FuncModel decides each Model call from the request it receives. Use it
// when a fixed script order would be racy: several agents sharing one Model,
// or an agent whose inputs arrive in a nondeterministic grouping. fn may
// block on ctx (for example on a test gate); an error it returns fails the
// call like a provider error.
type FuncModel struct {
	mu       sync.Mutex
	fn       func(context.Context, gotato.ModelRequest) (Script, error)
	requests []gotato.ModelRequest
}

// NewFuncModel creates a FuncModel.
func NewFuncModel(fn func(context.Context, gotato.ModelRequest) (Script, error)) *FuncModel {
	return &FuncModel{fn: fn}
}

// Stream implements gotato.Model. fn receives a copy of the request.
func (m *FuncModel) Stream(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	m.mu.Lock()
	m.requests = append(m.requests, request)
	m.mu.Unlock()
	events, err := m.fn(ctx, cloneRequest(request))
	if err != nil {
		return nil, err
	}
	return &stream{events: events}, nil
}

// Calls returns how many times Stream was called.
func (m *FuncModel) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

// Requests returns the ModelRequests received so far, with cloned Messages.
func (m *FuncModel) Requests() []gotato.ModelRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]gotato.ModelRequest, len(m.requests))
	for i, request := range m.requests {
		out[i] = cloneRequest(request)
	}
	return out
}

// ToolResults returns the text of every tool result in a request, in order:
// the result content, or its safe error when it has none.
func ToolResults(request gotato.ModelRequest) []string {
	var out []string
	for _, msg := range request.Messages {
		if msg.Role != gotato.RoleToolResult {
			continue
		}
		var b strings.Builder
		for _, p := range msg.Parts {
			b.WriteString(p.Text)
		}
		if b.Len() == 0 && msg.ToolResult != nil {
			for _, p := range msg.ToolResult.Content {
				b.WriteString(p.Text)
			}
			if b.Len() == 0 {
				b.WriteString(msg.ToolResult.SafeError)
			}
		}
		out = append(out, b.String())
	}
	return out
}

// UserText returns the text of every user Message in a request, one per
// line.
func UserText(request gotato.ModelRequest) string {
	var b strings.Builder
	for _, msg := range request.Messages {
		if msg.Role == gotato.RoleUser {
			b.WriteString(gotato.TextOf(msg))
			b.WriteByte('\n')
		}
	}
	return b.String()
}
