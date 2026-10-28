package gotato

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func storedEvent(number uint64) Event {
	return TurnStarted{Header: Header{Time: time.Now(), SessionID: "s", Number: number}}
}

// next reads one Event, failing the test when none comes.
func next(t *testing.T, sub Subscription) Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	event, err := sub.Next(ctx)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	return event
}

// journalTranscript is a Transcript that is a Journal: it numbers the Events
// it is given, keeps each write, and records the number of Messages committed
// before it. It fails a write that holds an Event of kind failAt with failErr.
// If hold is not nil, a write that holds a kind in holdKinds waits on it.
type journalTranscript struct {
	recordingTranscript
	mu        sync.Mutex
	writes    [][]Event
	before    []int
	last      uint64
	failAt    EventKind
	failErr   error
	hold      chan struct{}
	holdKinds map[EventKind]bool
	writing   chan struct{}
}

func (j *journalTranscript) Write(_ context.Context, events []Event) ([]Event, error) {
	committed := j.Len()
	for _, event := range events {
		if j.hold != nil && j.holdKinds[event.Kind()] {
			if j.writing != nil {
				j.writing <- struct{}{}
			}
			<-j.hold
		}
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, event := range events {
		if j.failAt != "" && event.Kind() == j.failAt {
			return nil, j.failErr
		}
	}
	out := make([]Event, len(events))
	for i, event := range events {
		j.last++
		out[i] = WithNumber(event, j.last)
	}
	j.writes = append(j.writes, out)
	j.before = append(j.before, committed)
	return out, nil
}

func (j *journalTranscript) kinds() []EventKind {
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []EventKind
	for _, write := range j.writes {
		for _, event := range write {
			out = append(out, event.Kind())
		}
	}
	return out
}

// shape names each write by the kinds it holds, joined by "+".
func (j *journalTranscript) shape() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []string
	for _, write := range j.writes {
		names := make([]string, len(write))
		for i, event := range write {
			names[i] = string(event.Kind())
		}
		out = append(out, strings.Join(names, "+"))
	}
	return out
}

func echoToolModel(calls ...string) *testModel {
	var first []ModelEvent
	for _, id := range calls {
		first = append(first, ModelEvent{Kind: ModelEventKindToolCall, ToolCall: &ToolCall{ID: ToolCallID(id), ToolID: "demo.echo", Arguments: []byte(`{"value":"x"}`)}})
	}
	first = append(first, ModelEvent{Kind: ModelEventKindDone, StopReason: StopReasonToolCalls})
	return &testModel{scripts: [][]ModelEvent{first, {{Kind: ModelEventKindTextDelta, Text: "done"}, {Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}}}}
}

// recorder reads a Session's stream on its own goroutine.
type recorder struct {
	t   *testing.T
	s   *Session
	sub Subscription
	mu  sync.Mutex
	got []Event
}

func recordAll(t *testing.T, s *Session) *recorder {
	t.Helper()
	sub, err := s.Subscribe(context.Background(), SubscribeBuffer(1<<14))
	if err != nil {
		t.Fatal(err)
	}
	r := &recorder{t: t, s: s, sub: sub}
	go func() {
		for {
			event, err := sub.Next(context.Background())
			if err != nil {
				return
			}
			r.mu.Lock()
			r.got = append(r.got, event)
			r.mu.Unlock()
		}
	}()
	t.Cleanup(func() { _ = sub.Close() })
	return r
}

// events returns the Events once the recorder has read all that the Session
// published.
func (r *recorder) events() []Event {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		count := uint64(len(r.got))
		r.mu.Unlock()
		if count >= r.s.Published() {
			break
		}
		time.Sleep(time.Millisecond)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.got...)
}

func (r *recorder) kinds() []EventKind {
	var out []EventKind
	for _, event := range r.events() {
		out = append(out, event.Kind())
	}
	return out
}

// recordingModel records the transcript it received on every call so a test can
// prove exactly which Turn a control Message reached the Model.
type recordingModel struct {
	mu       sync.Mutex
	requests [][]Message
	offered  [][]ToolSpec
	scripts  [][]ModelEvent
	started  chan int
	gate     chan struct{}
}

func (m *recordingModel) Stream(ctx context.Context, request ModelRequest) (ModelStream, error) {
	m.mu.Lock()
	index := len(m.requests)
	m.requests = append(m.requests, cloneMessages(request.Messages))
	m.offered = append(m.offered, cloneToolSpecs(request.Tools))
	if index >= len(m.scripts) {
		scripted := len(m.scripts)
		m.mu.Unlock()
		return nil, fmt.Errorf("recordingModel: unexpected Model call %d with only %d scripted response(s)", index, scripted)
	}
	events := m.scripts[index]
	m.mu.Unlock()
	if m.started != nil {
		m.started <- index
	}
	if m.gate != nil {
		<-m.gate
	}
	return &testStream{events: events}, nil
}

func (m *recordingModel) transcript(call int) []Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	if call >= len(m.requests) {
		return nil
	}
	return m.requests[call]
}

func (m *recordingModel) toolIDs(call int) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if call >= len(m.offered) {
		return nil
	}
	ids := make([]string, 0, len(m.offered[call]))
	for _, spec := range m.offered[call] {
		ids = append(ids, spec.ID)
	}
	return ids
}

func (m *recordingModel) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

func containsUserText(messages []Message, text string) bool {
	for _, message := range messages {
		if message.Role == RoleUser && strings.Contains(TextOf(message), text) {
			return true
		}
	}
	return false
}

func toolCallScript() []ModelEvent {
	return []ModelEvent{
		{Kind: ModelEventKindToolCall, ToolCall: &ToolCall{ID: "call-1", ToolID: "demo.echo", Arguments: []byte(`{"value":"x"}`)}},
		{Kind: ModelEventKindDone, StopReason: StopReasonToolCalls},
	}
}

func finalScript(text string) []ModelEvent {
	return []ModelEvent{{Kind: ModelEventKindTextDelta, Text: text}, {Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}}
}

func waitForCall(t *testing.T, started chan int, want int) {
	t.Helper()
	select {
	case got := <-started:
		if got != want {
			t.Fatalf("Model call index = %d, want %d", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Model call %d never started", want)
	}
}

type overflowError struct{ overflow bool }

func (e overflowError) Error() string         { return "refused: too long" }
func (e overflowError) ContextOverflow() bool { return e.overflow }

type countingTool struct {
	mu    sync.Mutex
	calls int
}

func (t *countingTool) Spec() ToolSpec {
	return ToolSpec{ID: "demo.echo", InputSchema: []byte(`{"type":"object","required":["value"],"properties":{"value":{"type":"string"}},"additionalProperties":false}`)}
}

func (t *countingTool) Execute(ctx context.Context, use ToolUse, progress ToolProgressFunc) (ToolResult, error) {
	t.mu.Lock()
	t.calls++
	t.mu.Unlock()
	return ToolResult{Content: []ContentPart{{Kind: ContentKindText, Text: "tool-ok"}}}, nil
}

func (t *countingTool) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls
}
