package gotato

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
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
	got := append([]Event(nil), r.got...)
	r.mu.Unlock()
	requireRunSpans(r.t, got)
	return got
}

// requireRunSpans fails the test when an Event that carries a RunID lies
// outside the span of that Run, between its run_started and its run_ended.
func requireRunSpans(t *testing.T, events []Event) {
	t.Helper()
	open := map[RunID]bool{}
	ended := map[RunID]bool{}
	for index, event := range events {
		run := event.Head().RunID
		switch {
		case run == "":
		case event.Kind() == EventKindRunStarted:
			open[run] = true
		case event.Kind() == EventKindRunEnded:
			if !open[run] {
				t.Errorf("event %d: run_ended of %s outside its span", index, run)
			}
			open[run], ended[run] = false, true
		case !open[run]:
			t.Errorf("event %d: %s of %s outside its run_started and run_ended", index, event.Kind(), run)
		}
	}
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

type namedTool struct {
	id      string
	hold    chan struct{}
	arrive  chan string
	tracker *concurrencyTracker
	seq     bool
}

func (t *namedTool) Spec() ToolSpec {
	return ToolSpec{
		ID:          t.id,
		Sequential:  t.seq,
		InputSchema: []byte(`{"type":"object","properties":{},"additionalProperties":false}`),
	}
}

func (t *namedTool) Execute(ctx context.Context, use ToolUse, progress ToolProgressFunc) (ToolResult, error) {
	if t.tracker != nil {
		t.tracker.enter()
		defer t.tracker.leave()
	}
	if t.arrive != nil {
		t.arrive <- t.id
	}
	if t.hold != nil {
		select {
		case <-t.hold:
		case <-ctx.Done():
			return ToolResult{}, ctx.Err()
		}
	}
	return ToolResult{Content: []ContentPart{{Kind: ContentKindText, Text: t.id}}}, nil
}

type concurrencyTracker struct {
	mu     sync.Mutex
	active int
	peak   int
}

func (c *concurrencyTracker) enter() {
	c.mu.Lock()
	c.active++
	if c.active > c.peak {
		c.peak = c.active
	}
	c.mu.Unlock()
}

func (c *concurrencyTracker) leave() {
	c.mu.Lock()
	c.active--
	c.mu.Unlock()
}

func (c *concurrencyTracker) activeValue() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active
}

func (c *concurrencyTracker) peakValue() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.peak
}

type stubToolSet struct {
	name  string
	tools []Tool
	err   error
	calls int
}

func (s *stubToolSet) Spec() ToolSetSpec { return ToolSetSpec{Name: s.name} }

func (s *stubToolSet) Tools(ctx context.Context) ([]Tool, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.tools, nil
}

// docTool is a Tool with a description that counts its runs.
type docTool struct {
	id, description string
	effect          ToolEffect
	runs            atomic.Int32
}

func (t *docTool) Spec() ToolSpec {
	return ToolSpec{
		ID: t.id, Description: t.description, Effect: t.effect,
		InputSchema: []byte(`{"type":"object","properties":{},"additionalProperties":false}`),
	}
}

func (t *docTool) Execute(context.Context, ToolUse, ToolProgressFunc) (ToolResult, error) {
	t.runs.Add(1)
	return ToolResult{Content: []ContentPart{{Kind: ContentKindText, Text: "ran " + t.id}}}, nil
}

type describedSet struct {
	name, description string
	tools             []Tool
}

func (s *describedSet) Spec() ToolSetSpec {
	return ToolSetSpec{Name: s.name, Description: s.description}
}
func (s *describedSet) Tools(context.Context) ([]Tool, error) { return s.tools, nil }

func ordersSet() (*describedSet, map[string]*docTool) {
	tools := map[string]*docTool{
		"lookup": {id: "lookup", description: "Look up one order by its number", effect: ToolEffectRead},
		"list":   {id: "list_open", description: "List the open orders of a customer", effect: ToolEffectRead},
		"refund": {id: "refund", description: "Refund a payment for an order", effect: ToolEffectWriteExternal},
	}
	return &describedSet{name: "orders", description: "Order lookup and refunds",
		tools: []Tool{tools["lookup"], tools["list"], tools["refund"]}}, tools
}

func searchCall(id string, arguments map[string]any) []ModelEvent {
	encoded, _ := json.Marshal(arguments)
	return []ModelEvent{
		{Kind: ModelEventKindToolCall, ToolCall: &ToolCall{ID: ToolCallID(id), ToolID: SearchToolName, Arguments: encoded}},
		{Kind: ModelEventKindDone, StopReason: StopReasonToolCalls},
	}
}

func callTool(id, toolID string) []ModelEvent {
	return []ModelEvent{
		{Kind: ModelEventKindToolCall, ToolCall: &ToolCall{ID: ToolCallID(id), ToolID: toolID, Arguments: []byte(`{}`)}},
		{Kind: ModelEventKindDone, StopReason: StopReasonToolCalls},
	}
}

func resultOf(t *testing.T, model *recordingModel, request int, callID string) ToolResult {
	t.Helper()
	model.mu.Lock()
	defer model.mu.Unlock()
	for _, message := range model.requests[request] {
		if message.ToolResult != nil && string(message.ToolResult.CallID) == callID {
			result := message.ToolResult.Clone()
			result.Content = cloneParts(message.Parts)
			return result
		}
	}
	t.Fatalf("request %d holds no result for %s", request, callID)
	return ToolResult{}
}

func textOfResult(result ToolResult) string { return TextOf(Message{Parts: result.Content}) }

func descriptionOf(model *recordingModel, request int, id string) string {
	model.mu.Lock()
	defer model.mu.Unlock()
	for _, spec := range model.offered[request] {
		if spec.ID == id {
			return spec.Description
		}
	}
	return ""
}

func newSearchSession(t *testing.T, model Model, options ...SessionOption) *Session {
	t.Helper()
	agent, err := NewSession(context.Background(), append([]SessionOption{WithModel(model)}, options...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = agent.Close(context.Background()) })
	return agent
}

// recordingTranscript is an external Transcript that records appends.
type recordingTranscript struct {
	mu       sync.Mutex
	messages []Message
	appends  int
}

func (t *recordingTranscript) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.messages)
}

func (t *recordingTranscript) Messages() []Message {
	t.mu.Lock()
	defer t.mu.Unlock()
	return cloneMessages(t.messages)
}

func (t *recordingTranscript) Append(message Message) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.messages = append(t.messages, message)
	t.appends++
	return nil
}

// bigTool returns a Result of size bytes of text.
type bigTool struct{ size int }

func (t bigTool) Spec() ToolSpec { return (&scriptedTool{}).Spec() }
func (t bigTool) Execute(context.Context, ToolUse, ToolProgressFunc) (ToolResult, error) {
	return ToolResult{Executed: true, Content: []ContentPart{{Kind: ContentKindText, Text: strings.Repeat("x", t.size)}}}, nil
}

type testStream struct {
	mu     sync.Mutex
	events []ModelEvent
	index  int
	block  <-chan struct{}
}

func (s *testStream) Recv(ctx context.Context) (ModelEvent, error) {
	s.mu.Lock()
	block := s.block
	s.mu.Unlock()
	if block != nil {
		select {
		case <-block:
			s.mu.Lock()
			s.block = nil
			s.mu.Unlock()
		case <-ctx.Done():
			return ModelEvent{}, ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.index >= len(s.events) {
		return ModelEvent{}, io.EOF
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}
func (s *testStream) Close() error { return nil }

type testModel struct {
	mu      sync.Mutex
	calls   int
	scripts [][]ModelEvent
	started chan struct{}
	block   <-chan struct{}
	last    ModelRequest
}

func (m *testModel) Stream(ctx context.Context, request ModelRequest) (ModelStream, error) {
	m.mu.Lock()
	index := m.calls
	m.calls++
	m.last = request
	if m.started != nil {
		select {
		case m.started <- struct{}{}:
		default:
		}
	}
	var events []ModelEvent
	if len(m.scripts) > 0 {
		events = m.scripts[min(index, len(m.scripts)-1)]
	}
	block := m.block
	m.mu.Unlock()
	return &testStream{events: events, block: block}, nil
}

type scriptedTool struct {
	calls int
	mu    sync.Mutex
}

func (t *scriptedTool) Spec() ToolSpec {
	return ToolSpec{ID: "demo.echo", InputSchema: []byte(`{"type":"object","required":["value"],"properties":{"value":{"type":"string"}},"additionalProperties":false}`)}
}
func (t *scriptedTool) Execute(ctx context.Context, use ToolUse, progress ToolProgressFunc) (ToolResult, error) {
	t.mu.Lock()
	t.calls++
	t.mu.Unlock()
	return ToolResult{Content: []ContentPart{{Kind: ContentKindText, Text: "tool-ok"}}}, nil
}

type toolModel struct {
	calls int
	mu    sync.Mutex
}

func (m *toolModel) Stream(ctx context.Context, request ModelRequest) (ModelStream, error) {
	m.mu.Lock()
	call := m.calls
	m.calls++
	m.mu.Unlock()
	if call == 0 {
		return &testStream{events: []ModelEvent{{Kind: ModelEventKindToolCall, ToolCall: &ToolCall{ID: "call-1", ToolID: "demo.echo", Arguments: []byte(`{"value":"x"}`)}}, {Kind: ModelEventKindDone, StopReason: StopReasonToolCalls}}}, nil
	}
	return &testStream{events: []ModelEvent{{Kind: ModelEventKindTextDelta, Text: "final"}, {Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}}}, nil
}

func (m *testModel) lastRequest() ModelRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.last
}
