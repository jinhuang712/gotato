// Package testkit provides deterministic doubles for testing Gotato runtimes
// without a network provider: scripted and replayed Models, a recording
// FakeTool, an EventRecorder over a Subscription, Session fixtures, the echo/demo
// Models used by the CLI and the reference service, and the tasks of the
// reference workload (ReferenceTasks).
package testkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/session"
)

// Script is the ModelEvent sequence one Model call streams.
type Script []gotato.ModelEvent

// Text builds a Script that streams text and ends the Turn.
func Text(text string) Script {
	return Script{
		{Kind: gotato.ModelTextDelta, Text: text},
		{Kind: gotato.ModelDone, StopReason: gotato.StopEndTurn},
	}
}

// ToolCalls builds a Script that requests the given Tool calls.
func ToolCalls(calls ...gotato.ToolCall) Script {
	out := make(Script, 0, len(calls)+1)
	for i := range calls {
		call := calls[i]
		out = append(out, gotato.ModelEvent{Kind: gotato.ModelToolCall, ToolCall: &call})
	}
	return append(out, gotato.ModelEvent{Kind: gotato.ModelDone, StopReason: gotato.StopToolCalls})
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
	return &stream{events: events, block: m.Block}, nil
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

// LoadReplay decodes a JSON array of Scripts (an array of arrays of
// gotato.ModelEvent).
func LoadReplay(data []byte) (*ReplayModel, error) {
	var scripts []Script
	if err := json.Unmarshal(data, &scripts); err != nil {
		return nil, err
	}
	return NewReplayModel(scripts...), nil
}

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
		Result: gotato.ToolResult{Status: gotato.ToolResultOK, Content: []gotato.ContentPart{{Kind: gotato.ContentText, Text: text}}},
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
	out.ArgumentsJSON = slices.Clone(use.ArgumentsJSON)
	return out
}

// Calls returns how many times Execute ran.
func (t *FakeTool) Calls() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.uses)
}

// EventRecorder keeps every Event of a Subscription in order, for a test to
// read. It reads the Subscription on a goroutine of its own, as any
// subscriber does, so the Session under test never waits for it.
type EventRecorder struct {
	mu        sync.Mutex
	changed   *sync.Cond
	events    []gotato.Event
	published func() uint64
	base      uint64
	sub       gotato.Subscription
	done      chan struct{}
}

// RecordEvents subscribes to a live Session and records every Event it
// publishes from now on, the stored Events and the deltas. Call it right after
// NewSession, before the first Prompt. Events waits until the recorder has
// read everything the Session has published, so a test can read the
// recording as soon as Prompt returns.
func RecordEvents(s *gotato.Session) *EventRecorder {
	sub, err := s.Subscribe(context.Background(), gotato.SubscribeBuffer(1<<16))
	if err != nil {
		panic("testkit: " + err.Error())
	}
	r := newRecorder(sub)
	r.published, r.base = s.Published, s.Published()
	return r
}

// RecordSubscription records every Event of sub until it ends. Events returns
// what has arrived so far; Wait waits for more.
func RecordSubscription(sub gotato.Subscription) *EventRecorder { return newRecorder(sub) }

func newRecorder(sub gotato.Subscription) *EventRecorder {
	r := &EventRecorder{sub: sub, done: make(chan struct{})}
	r.changed = sync.NewCond(&r.mu)
	go func() {
		defer close(r.done)
		defer func() {
			r.mu.Lock()
			r.changed.Broadcast()
			r.mu.Unlock()
		}()
		for {
			event, err := sub.Next(context.Background())
			if err != nil {
				return
			}
			r.mu.Lock()
			r.events = append(r.events, event)
			r.changed.Broadcast()
			r.mu.Unlock()
		}
	}()
	return r
}

// Events returns the recorded Events, in the order they were published.
func (r *EventRecorder) Events() []gotato.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.published != nil {
		deadline := time.Now().Add(5 * time.Second)
		for uint64(len(r.events)) < r.published()-r.base && time.Now().Before(deadline) {
			r.mu.Unlock()
			time.Sleep(time.Millisecond)
			r.mu.Lock()
		}
	}
	return slices.Clone(r.events)
}

// Wait blocks until the recording holds an Event for which match is true, or
// timeout passes, and reports whether it found one.
func (r *EventRecorder) Wait(timeout time.Duration, match func(gotato.Event) bool) bool {
	deadline := time.Now().Add(timeout)
	timer := time.AfterFunc(timeout, func() {
		r.mu.Lock()
		r.changed.Broadcast()
		r.mu.Unlock()
	})
	defer timer.Stop()
	r.mu.Lock()
	defer r.mu.Unlock()
	for {
		if slices.ContainsFunc(r.events, match) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		select {
		case <-r.done:
			return slices.ContainsFunc(r.events, match)
		default:
		}
		r.changed.Wait()
	}
}

// Close ends the Subscription and the recorder's goroutine.
func (r *EventRecorder) Close() {
	_ = r.sub.Close()
	<-r.done
}

// Kinds returns the recorded Event kinds in order.
func (r *EventRecorder) Kinds() []gotato.EventKind {
	events := r.Events()
	out := make([]gotato.EventKind, len(events))
	for i, event := range events {
		out[i] = event.Kind()
	}
	return out
}

// OfKind returns the recorded Events of one kind.
func (r *EventRecorder) OfKind(kind gotato.EventKind) []gotato.Event {
	var out []gotato.Event
	for _, event := range r.Events() {
		if event.Kind() == kind {
			out = append(out, event)
		}
	}
	return out
}

// Reset drops recorded Events.
func (r *EventRecorder) Reset() {
	r.mu.Lock()
	r.events = nil
	if r.published != nil {
		r.base = r.published()
	}
	r.mu.Unlock()
}

// NewRecord creates a Session Record pre-populated with alternating user and
// assistant text Messages, e.g. NewRecord("hi", "hello", "how are you?").
func NewRecord(turns ...string) *session.Record {
	s := session.New()
	for i, text := range turns {
		var message gotato.Message
		if i%2 == 0 {
			message = gotato.UserMessage(text)
		} else {
			message = gotato.AssistantMessage(text)
			message.StopReason = gotato.StopEndTurn
		}
		message.ID = gotato.MessageID("fixture-" + itoa(i+1))
		_ = s.Append(message)
	}
	return s
}

// EchoModel answers "echo: <last user text>" and ends the Turn. It is the
// CLI's default Model.
type EchoModel struct{}

// Stream implements gotato.Model.
func (EchoModel) Stream(_ context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	text := ""
	for i := len(request.Messages) - 1; i >= 0; i-- {
		if request.Messages[i].Role == gotato.RoleUser {
			text = promptText(request.Messages[i])
			break
		}
	}
	return &stream{events: Text("echo: " + text)}, nil
}

// DemoModel exercises the Tool loop deterministically: when the last user
// text is "use-tool" and no tool result follows it, it calls demo.echo with
// {"value":"from-tool"}; otherwise it answers "demo response: <text>".
type DemoModel struct{}

// DemoToolID is the Tool DemoModel calls.
const DemoToolID = "demo.echo"

// Stream implements gotato.Model.
func (DemoModel) Stream(_ context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	lastUser := ""
	hasToolResult := false
	for i := len(request.Messages) - 1; i >= 0; i-- {
		message := request.Messages[i]
		if message.Role == gotato.RoleToolResult {
			hasToolResult = true
		}
		if message.Role == gotato.RoleUser && lastUser == "" {
			lastUser = promptText(message)
			break
		}
	}
	if strings.TrimSpace(lastUser) == "use-tool" && !hasToolResult {
		return &stream{events: ToolCalls(gotato.ToolCall{ID: "call-1", ToolID: DemoToolID, Arguments: []byte(`{"value":"from-tool"}`)})}, nil
	}
	return &stream{events: Text("demo response: " + lastUser)}, nil
}

// promptText returns the first text part of a Message: the user's prompt.
func promptText(message gotato.Message) string {
	for _, part := range message.Parts {
		if part.Kind == gotato.ContentText {
			return part.Text
		}
	}
	return ""
}

// DemoEchoTool is the Tool DemoModel calls: it returns its "value" argument.
func DemoEchoTool() gotato.Tool {
	tool, err := gotato.NewFuncTool(DemoToolID, "Returns the value passed to it.", func(_ context.Context, in struct {
		Value string `json:"value" description:"text to echo back"`
	}) (string, error) {
		return in.Value, nil
	})
	if err != nil {
		panic(err)
	}
	return tool
}

// ErrScriptExhausted is returned by ReplayModel when the recording runs out.
// It wraps io.ErrUnexpectedEOF, so errors.Is matches either sentinel.
var ErrScriptExhausted = fmt.Errorf("testkit: script exhausted: %w", io.ErrUnexpectedEOF)

func itoa(n int) string { return strconv.Itoa(n) }
