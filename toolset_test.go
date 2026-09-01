package gotato

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

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

func (t *namedTool) Execute(ctx context.Context, use ToolUse, progress ToolProgress) (ToolResult, error) {
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
	return ToolResult{Content: []ContentPart{{Kind: ContentText, Text: t.id}}}, nil
}

// awaitArrivals blocks until the named number of Tools are inside their
// executors at the same time. It is the deterministic proof that a batch ran
// in parallel: with one worker it never completes.
func awaitArrivals(t *testing.T, arrive chan string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		select {
		case <-arrive:
		case <-time.After(3 * time.Second):
			t.Fatalf("only %d of %d Tools were running at once", i, count)
		}
	}
}

type concurrencyTracker struct {
	mu      sync.Mutex
	active  int
	peak    int
	overlap map[string]bool
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

func activationScript(name string) []ModelEvent {
	arguments, _ := json.Marshal(map[string]string{"name": name})
	return []ModelEvent{
		{Kind: ModelToolCall, ToolCall: &ToolCall{ID: "call-activate", ToolID: ActivationToolName, Arguments: arguments}},
		{Kind: ModelDone, StopReason: StopToolCalls},
	}
}

func TestToolSetStaysHiddenUntilTheNextRequest(t *testing.T) {
	set := &stubToolSet{name: "files", tools: []Tool{&namedTool{id: "read"}, &namedTool{id: "write"}}}
	model := &recordingModel{scripts: [][]ModelEvent{activationScript("files"), finalScript("done")}}
	agent, err := NewAgent(WithModel(model), WithToolSet(set))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	events, err := agent.(EventSource).Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()

	if _, err := agent.Prompt(context.Background(), UserMessage("open the files")); err != nil {
		t.Fatal(err)
	}
	first := model.toolIDs(0)
	if !slices.Contains(first, ActivationToolName) {
		t.Fatalf("first request tools = %v", first)
	}
	if slices.Contains(first, "files.read") {
		t.Fatalf("inactive ToolSet leaked into the first request: %v", first)
	}
	second := model.toolIDs(1)
	if !slices.Contains(second, "files.read") || !slices.Contains(second, "files.write") {
		t.Fatalf("activated ToolSet missing from the next request: %v", second)
	}
	if slices.Contains(second, ActivationToolName) {
		t.Fatalf("activation Tool stayed visible with no inactive ToolSet: %v", second)
	}

	activations := 0
	for {
		event, nextErr := events.Next(context.Background())
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		if event.Kind == EventToolSetActivated {
			if event.Class != EventProtected || event.Payload["toolset"] != "files" {
				t.Fatalf("activation event = %+v", event)
			}
			activations++
		}
		if event.Kind == EventAgentEnd {
			break
		}
	}
	if activations != 1 {
		t.Fatalf("activation events = %d", activations)
	}
}

func TestToolSetActivationIsIdempotent(t *testing.T) {
	files := &stubToolSet{name: "files", tools: []Tool{&namedTool{id: "read"}}}
	// Two activation calls for the same ToolSet inside one batch: this is
	// the race idempotency has to survive. Across batches the ToolSet is no
	// longer in the activation enum, so a repeat call is an argument error.
	arguments, _ := json.Marshal(map[string]string{"name": "files"})
	batch := []ModelEvent{
		{Kind: ModelToolCall, ToolCall: &ToolCall{ID: "call-a", ToolID: ActivationToolName, Arguments: arguments}},
		{Kind: ModelToolCall, ToolCall: &ToolCall{ID: "call-b", ToolID: ActivationToolName, Arguments: arguments}},
		{Kind: ModelDone, StopReason: StopToolCalls},
	}
	model := &recordingModel{scripts: [][]ModelEvent{batch, finalScript("done")}}
	agent, err := NewAgent(WithModel(model), WithToolSet(files))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	if _, err := agent.Prompt(context.Background(), UserMessage("open")); err != nil {
		t.Fatal(err)
	}
	next := model.toolIDs(1)
	occurrences := 0
	for _, id := range next {
		if id == "files.read" {
			occurrences++
		}
	}
	if occurrences != 1 {
		t.Fatalf("duplicate activation changed visibility: %v", next)
	}
	if files.calls != 1 {
		t.Fatalf("ToolSet resolved %d times for two activation calls", files.calls)
	}
}

func TestFailedToolSetResolutionExposesNothing(t *testing.T) {
	set := &stubToolSet{name: "files", err: errors.New("catalog is down")}
	model := &recordingModel{scripts: [][]ModelEvent{activationScript("files"), finalScript("done")}}
	agent, err := NewAgent(WithModel(model), WithToolSet(set))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	if _, err := agent.Prompt(context.Background(), UserMessage("open")); err != nil {
		t.Fatal(err)
	}
	snapshot, err := agent.(Snapshotter).Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	result := snapshot.Messages[2].ToolResult
	if result.Status != ToolResultFailed || !strings.Contains(result.SafeError, "catalog is down") {
		t.Fatalf("failed activation result = %+v", result)
	}
	second := model.toolIDs(1)
	if slices.Contains(second, "files.read") {
		t.Fatalf("failed ToolSet exposed Tools: %v", second)
	}
	if !slices.Contains(second, ActivationToolName) {
		t.Fatalf("activation Tool disappeared after a failure: %v", second)
	}
}

func TestToolSetConstructionValidation(t *testing.T) {
	model := &recordingModel{scripts: [][]ModelEvent{finalScript("x")}}
	if _, err := NewAgent(WithModel(model),
		WithActiveToolSet(&stubToolSet{name: "files", tools: []Tool{&namedTool{id: "read"}}}),
		WithActiveToolSet(&stubToolSet{name: "files", tools: []Tool{&namedTool{id: "write"}}}),
	); !IsCode(err, ErrInvalidArgument) {
		t.Fatalf("duplicate ToolSet name = %v", err)
	}
	if _, err := NewAgent(WithModel(model),
		WithActiveToolSet(&stubToolSet{name: "files.deep", tools: []Tool{&namedTool{id: "read"}}}),
	); !IsCode(err, ErrInvalidArgument) {
		t.Fatalf("dotted ToolSet name = %v", err)
	}
	if _, err := NewAgent(WithModel(model),
		WithActiveToolSet(&stubToolSet{name: "files", tools: []Tool{&namedTool{id: "read"}, &namedTool{id: "read"}}}),
	); !IsCode(err, ErrInvalidArgument) {
		t.Fatalf("duplicate local Tool name = %v", err)
	}
	if _, err := NewAgent(WithModel(model), WithToolSet(nil)); !IsCode(err, ErrInvalidArgument) {
		t.Fatalf("nil ToolSet = %v", err)
	}
	if _, err := NewAgent(WithModel(model), WithRootNamespace("a.b")); !IsCode(err, ErrInvalidArgument) {
		t.Fatalf("dotted root namespace = %v", err)
	}
	agent, err := NewAgent(WithModel(model), WithRootNamespace("core"), WithTool(&namedTool{id: "ping"}))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	if _, err := agent.Prompt(context.Background(), UserMessage("hi")); err != nil {
		t.Fatal(err)
	}
	if ids := model.toolIDs(0); !slices.Contains(ids, "core.ping") {
		t.Fatalf("root namespace not applied: %v", ids)
	}
}

func parallelCallScript(ids ...string) []ModelEvent {
	events := make([]ModelEvent, 0, len(ids)+1)
	for i, id := range ids {
		events = append(events, ModelEvent{Kind: ModelToolCall, ToolCall: &ToolCall{
			ID:        ToolCallID("call-" + id),
			ToolID:    id,
			Arguments: []byte(`{}`),
		}})
		_ = i
	}
	return append(events, ModelEvent{Kind: ModelDone, StopReason: StopToolCalls})
}

func TestParallelToolsCommitInSourceOrder(t *testing.T) {
	tracker := &concurrencyTracker{}
	arrive := make(chan string, 2)
	fastRelease := make(chan struct{})
	slowRelease := make(chan struct{})
	first := &namedTool{id: "slow", arrive: arrive, hold: slowRelease, tracker: tracker}
	second := &namedTool{id: "fast", arrive: arrive, hold: fastRelease, tracker: tracker}
	limits := defaultLimits()
	limits.MaxParallelTools = 2
	model := &recordingModel{scripts: [][]ModelEvent{parallelCallScript("slow", "fast"), finalScript("done")}}
	agent, err := NewAgent(WithModel(model), WithTools(first, second), WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	events, err := agent.(EventSource).Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()

	// Reading Events in step with the batch keeps the completion order
	// deterministic: "slow" is released only after the first completion Event
	// has already been emitted, and only "fast" can have produced it.
	var mu sync.Mutex
	var ends, commits []ToolCallID
	firstEnd := make(chan struct{})
	observed := make(chan error, 1)
	go func() {
		var once sync.Once
		for {
			event, nextErr := events.Next(context.Background())
			if nextErr != nil {
				observed <- nextErr
				return
			}
			mu.Lock()
			switch event.Kind {
			case EventToolExecutionEnd:
				ends = append(ends, event.ToolCallID)
				once.Do(func() { close(firstEnd) })
			case EventToolResultCommitted:
				commits = append(commits, event.ToolCallID)
			}
			mu.Unlock()
			if event.Kind == EventAgentEnd {
				observed <- nil
				return
			}
		}
	}()

	done := make(chan error, 1)
	go func() {
		_, runErr := agent.Prompt(context.Background(), UserMessage("run both"))
		done <- runErr
	}()
	awaitArrivals(t, arrive, 2)
	close(fastRelease)
	select {
	case <-firstEnd:
	case <-time.After(3 * time.Second):
		t.Fatal("no completion Event arrived while one Tool was still held")
	}
	close(slowRelease)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("parallel batch did not settle")
	}
	select {
	case err := <-observed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Event stream did not reach agent_end")
	}
	if tracker.peakValue() != 2 {
		t.Fatalf("parallel peak = %d", tracker.peakValue())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(ends) != 2 || ends[0] != "call-fast" {
		t.Fatalf("completion Events did not follow completion order: %v", ends)
	}
	if len(commits) != 2 || commits[0] != "call-slow" || commits[1] != "call-fast" {
		t.Fatalf("commitment did not follow source order: %v", commits)
	}
	snapshot, err := agent.(Snapshotter).Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if TextOf(snapshot.Messages[2]) != "slow" || TextOf(snapshot.Messages[3]) != "fast" {
		t.Fatalf("transcript order = %q, %q", TextOf(snapshot.Messages[2]), TextOf(snapshot.Messages[3]))
	}
}

func TestSequentialToolRunsAlone(t *testing.T) {
	tracker := &concurrencyTracker{}
	hold := make(chan struct{})
	close(hold)
	limits := defaultLimits()
	limits.MaxParallelTools = 4
	tools := []Tool{
		&namedTool{id: "a", tracker: tracker},
		&namedTool{id: "guard", tracker: tracker, seq: true},
		&namedTool{id: "b", tracker: tracker},
	}
	model := &recordingModel{scripts: [][]ModelEvent{parallelCallScript("a", "guard", "b"), finalScript("done")}}
	agent, err := NewAgent(WithModel(model), WithTools(tools...), WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	if _, err := agent.Prompt(context.Background(), UserMessage("run")); err != nil {
		t.Fatal(err)
	}
	if tracker.peakValue() > 1 {
		t.Fatalf("a Sequential Tool ran alongside others: peak = %d", tracker.peakValue())
	}
}

func TestParallelWorkerBoundIsRespected(t *testing.T) {
	tracker := &concurrencyTracker{}
	arrive := make(chan string, 4)
	release := make(chan struct{})
	limits := defaultLimits()
	limits.MaxParallelTools = 2
	tools := []Tool{
		&namedTool{id: "one", arrive: arrive, hold: release, tracker: tracker},
		&namedTool{id: "two", arrive: arrive, hold: release, tracker: tracker},
		&namedTool{id: "three", arrive: arrive, hold: release, tracker: tracker},
		&namedTool{id: "four", arrive: arrive, hold: release, tracker: tracker},
	}
	model := &recordingModel{scripts: [][]ModelEvent{parallelCallScript("one", "two", "three", "four"), finalScript("done")}}
	agent, err := NewAgent(WithModel(model), WithTools(tools...), WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	done := make(chan error, 1)
	go func() {
		_, runErr := agent.Prompt(context.Background(), UserMessage("run"))
		done <- runErr
	}()

	// Exactly two Tools may be inside their executors; a third must wait for
	// the first group to settle.
	awaitArrivals(t, arrive, 2)
	select {
	case id := <-arrive:
		t.Fatalf("Tool %q started while the worker bound was already full", id)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	awaitArrivals(t, arrive, 2)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("bounded batch did not settle")
	}
	if tracker.peakValue() != 2 {
		t.Fatalf("worker bound not exercised: peak = %d", tracker.peakValue())
	}
}
