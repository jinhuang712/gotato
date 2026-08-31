package testmodel

import (
	"context"
	"io"
	"strings"

	gotato "github.com/jinhuang712/gotato"
)

type EchoModel struct{}

func (EchoModel) Stream(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	text := ""
	for i := len(request.Messages) - 1; i >= 0; i-- {
		if request.Messages[i].Role == gotato.RoleUser {
			text = gotato.TextOf(request.Messages[i])
			break
		}
	}
	return &stream{events: []gotato.ModelEvent{
		{Kind: gotato.ModelTextDelta, Text: "echo: " + text},
		{Kind: gotato.ModelDone, StopReason: gotato.StopEndTurn},
	}}, nil
}

type DemoModel struct{}

func (DemoModel) Stream(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	lastUser := ""
	hasToolResult := false
	for i := len(request.Messages) - 1; i >= 0; i-- {
		message := request.Messages[i]
		if message.Role == gotato.RoleToolResult {
			hasToolResult = true
		}
		if message.Role == gotato.RoleUser && lastUser == "" {
			lastUser = gotato.TextOf(message)
		}
	}
	if strings.TrimSpace(lastUser) == "use-tool" && !hasToolResult {
		return &stream{events: []gotato.ModelEvent{
			{Kind: gotato.ModelToolCall, ToolCall: &gotato.ToolCall{ID: "call-1", ToolID: "demo.echo", Arguments: []byte(`{"value":"from-tool"}`)}},
			{Kind: gotato.ModelDone, StopReason: gotato.StopToolCalls},
		}}, nil
	}
	return &stream{events: []gotato.ModelEvent{
		{Kind: gotato.ModelTextDelta, Text: "demo response: " + lastUser},
		{Kind: gotato.ModelDone, StopReason: gotato.StopEndTurn},
	}}, nil
}

type stream struct {
	events []gotato.ModelEvent
	index  int
	closed bool
}

func (s *stream) Recv(ctx context.Context) (gotato.ModelEvent, error) {
	if err := ctx.Err(); err != nil {
		return gotato.ModelEvent{}, err
	}
	if s.closed || s.index >= len(s.events) {
		return gotato.ModelEvent{}, io.EOF
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (s *stream) Close() error { s.closed = true; return nil }
