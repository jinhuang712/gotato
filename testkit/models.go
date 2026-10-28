package testkit

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// Script is the ModelEvent sequence one Model call streams.
type Script []gotato.ModelEvent

// Text builds a Script that streams text and ends the Turn.
func Text(text string) Script {
	return Script{
		{Kind: gotato.ModelEventKindTextDelta, Text: text},
		{Kind: gotato.ModelEventKindDone, StopReason: gotato.StopReasonEndTurn},
	}
}

// ToolCalls builds a Script that requests the given Tool calls.
func ToolCalls(calls ...gotato.ToolCall) Script {
	out := make(Script, 0, len(calls)+1)
	for i := range calls {
		call := calls[i]
		out = append(out, gotato.ModelEvent{Kind: gotato.ModelEventKindToolCall, ToolCall: &call})
	}
	return append(out, gotato.ModelEvent{Kind: gotato.ModelEventKindDone, StopReason: gotato.StopReasonToolCalls})
}

// FakeModel streams one Script per call, in order. After the last Script it
// repeats the last one, so a loop always terminates deterministically. It
// records every ModelRequest it received.
type FakeModel struct {
	mu       sync.Mutex
	scripts  []Script
	requests []gotato.ModelRequest
	// Err, when set, is returned by Stream instead of a Script.
	Err error
	// Block, when set, makes Recv wait until the channel is closed or the
	// context ends. Use it to test cancellation deterministically.
	Block <-chan struct{}
	// Delay, when set, makes Recv wait this long before every event (or until
	// the context ends), so a test can read a stream while it is still open:
	// a client that disconnects mid-stream, a resume, a cancellation.
	Delay time.Duration
}

// NewFakeModel creates a FakeModel with the given Scripts.
func NewFakeModel(scripts ...Script) *FakeModel { return &FakeModel{scripts: scripts} }

// Stream implements gotato.Model.
func (m *FakeModel) Stream(_ context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, request)
	if m.Err != nil {
		return nil, m.Err
	}
	var events Script
	if len(m.scripts) == 0 {
		return nil, errors.New("testkit: FakeModel has no scripts configured")
	}
	index := len(m.requests) - 1
	if index >= len(m.scripts) {
		index = len(m.scripts) - 1
	}
	events = m.scripts[index]
	return &stream{events: events, block: m.Block, delay: m.Delay}, nil
}

// Calls returns how many times Stream was called.
func (m *FakeModel) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

// Requests returns the ModelRequests received so far, with cloned Messages.
func (m *FakeModel) Requests() []gotato.ModelRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]gotato.ModelRequest, len(m.requests))
	for i, request := range m.requests {
		out[i] = cloneRequest(request)
	}
	return out
}

// LastRequest returns the most recent ModelRequest, if any.
func (m *FakeModel) LastRequest() (gotato.ModelRequest, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.requests) == 0 {
		return gotato.ModelRequest{}, false
	}
	return cloneRequest(m.requests[len(m.requests)-1]), true
}

// cloneRequest deep-copies the Message list so a caller cannot mutate the
// recording the fake keeps.
func cloneRequest(request gotato.ModelRequest) gotato.ModelRequest {
	out := request
	out.Messages = make([]gotato.Message, len(request.Messages))
	for i, message := range request.Messages {
		out.Messages[i] = message.Clone()
	}
	return out
}

// ReplayModel replays recorded ModelEvent sequences. Unlike FakeModel it
// fails with io.ErrUnexpectedEOF once the recording is exhausted, so a test
// notices when the loop makes more Model calls than were recorded.
type ReplayModel struct {
	mu      sync.Mutex
	scripts []Script
	calls   int
}

// NewReplayModel creates a ReplayModel from Scripts.
func NewReplayModel(scripts ...Script) *ReplayModel { return &ReplayModel{scripts: scripts} }

// Stream implements gotato.Model.
func (m *ReplayModel) Stream(context.Context, gotato.ModelRequest) (gotato.ModelStream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.calls >= len(m.scripts) {
		return nil, ErrScriptExhausted
	}
	events := m.scripts[m.calls]
	m.calls++
	return &stream{events: events}, nil
}

// Remaining reports how many recorded calls are left.
func (m *ReplayModel) Remaining() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.scripts) - m.calls
}

type stream struct {
	events []gotato.ModelEvent
	index  int
	block  <-chan struct{}
	delay  time.Duration
}

func (s *stream) Recv(ctx context.Context) (gotato.ModelEvent, error) {
	if s.block != nil {
		select {
		case <-s.block:
			s.block = nil
		case <-ctx.Done():
			return gotato.ModelEvent{}, ctx.Err()
		}
	}
	if s.delay > 0 && s.index < len(s.events) {
		timer := time.NewTimer(s.delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return gotato.ModelEvent{}, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return gotato.ModelEvent{}, err
	}
	if s.index >= len(s.events) {
		return gotato.ModelEvent{}, io.EOF
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (s *stream) Close() error { return nil }
