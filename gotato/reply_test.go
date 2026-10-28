package gotato

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

// endlessModel streams events from next without sending Done (for 2000 events), and counts
// what the Session read and whether it closed the stream.
type endlessModel struct {
	next     func(n int) ModelEvent
	received atomic.Int32
	closed   atomic.Bool
}

func (m *endlessModel) Stream(context.Context, ModelRequest) (ModelStream, error) {
	return &endlessStream{m: m}, nil
}

type endlessStream struct{ m *endlessModel }

func (s *endlessStream) Recv(ctx context.Context) (ModelEvent, error) {
	if err := ctx.Err(); err != nil {
		return ModelEvent{}, err
	}
	n := int(s.m.received.Add(1))
	if n > 2000 { // a Session that does not stop the reply still ends the test
		return ModelEvent{Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}, nil
	}
	return s.m.next(n), nil
}

func (s *endlessStream) Close() error { s.m.closed.Store(true); return nil }

// A reply is bounded by MaxMessageBytes while it streams, not only when it is
// committed: a Model that never sends Done is stopped, and the call is closed,
// within a delta of the limit.
func TestReplyIsBoundedWhileItStreams(t *testing.T) {
	const limit = 1024
	chunk := strings.Repeat("x", 100)
	cases := map[string]func(n int) ModelEvent{
		"text":      func(int) ModelEvent { return ModelEvent{Kind: ModelEventKindTextDelta, Text: chunk} },
		"reasoning": func(int) ModelEvent { return ModelEvent{Kind: ModelEventKindReasoningDelta, Text: chunk} },
		"one delta": func(int) ModelEvent {
			return ModelEvent{Kind: ModelEventKindTextDelta, Text: strings.Repeat("x", 10*limit)}
		},
		"tool call arguments": func(n int) ModelEvent {
			call := ToolCall{ID: ToolCallID("c" + string(rune('a'+n%26)) + strings.Repeat("0", n/26)), ToolID: "t", Arguments: []byte(`"` + chunk + `"`)}
			return ModelEvent{Kind: ModelEventKindToolCall, ToolCall: &call}
		},
		"reasoning artifact": func(int) ModelEvent {
			return ModelEvent{Kind: ModelEventKindReasoningDone, ReasoningArtifact: []byte(chunk)}
		},
	}
	for name, next := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			m := &endlessModel{next: next}
			s, err := NewSession(ctx, WithModel(m), WithLimits(CoreLimits{MaxMessageBytes: limit}))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close(ctx) }()
			_, err = s.Prompt(ctx, UserMessage("go"))
			var re *RuntimeError
			if !errors.As(err, &re) || re.Code != CodeLimitExceeded || re.Details["limit"] != limitMessageBytes {
				t.Fatalf("err = %v, want limit_exceeded on MaxMessageBytes", err)
			}
			if got := m.received.Load(); got > limit/100+2 {
				t.Fatalf("read %d events of 100 bytes against a limit of %d bytes", got, limit)
			}
			if !m.closed.Load() {
				t.Fatal("the model stream was not closed")
			}
		})
	}
}
