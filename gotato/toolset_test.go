package gotato

import (
	"context"
	"errors"
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
	return ToolResult{Content: []ContentPart{{Kind: ContentText, Text: t.id}}}, nil
}

// awaitArrivals blocks until the named number of Tools are inside their
// executors at the same time. It is the deterministic proof that a batch ran
// in parallel: with one worker it never completes.
func awaitArrivals(t *testing.T, arrive chan string, count int) {
	t.Helper()
	for i := range count {
		select {
		case <-arrive:
		case <-time.After(3 * time.Second):
			t.Fatalf("only %d of %d Tools were running at once", i, count)
		}
	}
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

func TestFailedToolSetResolutionDoesNotFailNewSession(t *testing.T) {
	set := &stubToolSet{name: "files", err: errors.New("catalog is down")}
	model := &recordingModel{scripts: [][]ModelEvent{finalScript("done")}}
	agent, err := NewSession(WithModel(model), WithToolSet(set))
	if err != nil {
		t.Fatalf("a ToolSet that does not resolve failed NewSession: %v", err)
	}
	_ = agent.Close(context.Background())
}

func TestToolSetConstructionValidation(t *testing.T) {
	model := &recordingModel{scripts: [][]ModelEvent{finalScript("x")}}
	if _, err := NewSession(WithModel(model),
		WithActiveToolSet(&stubToolSet{name: "files", tools: []Tool{&namedTool{id: "read"}}}),
		WithActiveToolSet(&stubToolSet{name: "files", tools: []Tool{&namedTool{id: "write"}}}),
	); !IsCode(err, ErrInvalidArgument) {
		t.Fatalf("duplicate ToolSet name = %v", err)
	}
	if _, err := NewSession(WithModel(model),
		WithActiveToolSet(&stubToolSet{name: "files.deep", tools: []Tool{&namedTool{id: "read"}}}),
	); !IsCode(err, ErrInvalidArgument) {
		t.Fatalf("dotted ToolSet name = %v", err)
	}
	// A ToolSet that returns a duplicate local Tool name is unavailable.
	dup := &stubToolSet{name: "files", tools: []Tool{&namedTool{id: "read"}, &namedTool{id: "read"}}}
	if agent, err := NewSession(WithModel(model), WithActiveToolSet(dup)); err != nil {
		t.Fatalf("duplicate local Tool name = %v", err)
	} else {
		if agent.registry.sets[0].failure == nil {
			t.Fatal("a ToolSet with a duplicate local Tool name is not marked unavailable")
		}
		_ = agent.Close(context.Background())
	}
	if _, err := NewSession(WithModel(model), WithToolSet(nil)); !IsCode(err, ErrInvalidArgument) {
		t.Fatalf("nil ToolSet = %v", err)
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
	agent, err := NewSession(WithModel(model), WithTools(first, second), WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	events, err := agent.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = events.Close() }()

	// The Events of a step are written together, so a ToolEnded is sent only
	// after the whole group has settled. To keep the completion order
	// deterministic, "slow" is released only after "fast" has left its
	// executor and had time to report; the stored ToolEnded Events must follow
	// that completion order, while the Tool Results commit in source order.
	var mu sync.Mutex
	var ends, commits []ToolCallID
	observed := make(chan error, 1)
	go func() {
		for {
			event, nextErr := events.Next(context.Background())
			if nextErr != nil {
				observed <- nextErr
				return
			}
			mu.Lock()
			switch e := event.(type) {
			case ToolEnded:
				ends = append(ends, e.CallID)
			case ToolResultCommitted:
				commits = append(commits, e.CallID)
			}
			mu.Unlock()
			if event.Kind() == KindRunEnded {
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
	deadline := time.Now().Add(10 * time.Second)
	for tracker.activeValue() != 1 {
		if time.Now().After(deadline) {
			t.Fatal("the released Tool did not finish while the other was held")
		}
		time.Sleep(time.Millisecond)
	}
	// No Event shows the fast Tool's outcome before the whole group settles,
	// so there is nothing to wait on: the Session goroutine stages it from a
	// channel the worker has already sent on, and this margin covers that hop.
	time.Sleep(50 * time.Millisecond)
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
		t.Fatal("Event stream did not reach run_ended")
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
}

func TestSequentialToolRunsAlone(t *testing.T) {
	tracker := &concurrencyTracker{}
	arrive := make(chan string, 3)
	aRelease := make(chan struct{})
	guardRelease := make(chan struct{})
	bRelease := make(chan struct{})
	limits := defaultLimits()
	limits.MaxParallelTools = 4
	tools := []Tool{
		&namedTool{id: "a", arrive: arrive, hold: aRelease, tracker: tracker},
		&namedTool{id: "guard", arrive: arrive, hold: guardRelease, tracker: tracker, seq: true},
		&namedTool{id: "b", arrive: arrive, hold: bRelease, tracker: tracker},
	}
	model := &recordingModel{scripts: [][]ModelEvent{parallelCallScript("a", "guard", "b"), finalScript("done")}}
	agent, err := NewSession(WithModel(model), WithTools(tools...), WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()

	done := make(chan error, 1)
	go func() {
		_, runErr := agent.Prompt(context.Background(), UserMessage("run"))
		done <- runErr
	}()

	// The Sequential guard splits the batch into single-Tool groups. Each Tool
	// must therefore be observed in its executor alone: a second arrival while
	// one is held would mean the guard ran alongside another Tool.
	expectArrival := func(want string) {
		t.Helper()
		select {
		case got := <-arrive:
			if got != want {
				t.Fatalf("Tool arrival = %q, want %q", got, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("Tool %q did not start", want)
		}
	}
	assertAlone := func() {
		t.Helper()
		select {
		case got := <-arrive:
			t.Fatalf("Tool %q started while another was still running", got)
		case <-time.After(100 * time.Millisecond):
		}
	}

	expectArrival("a")
	assertAlone()
	close(aRelease)
	expectArrival("guard")
	assertAlone()
	close(guardRelease)
	expectArrival("b")
	close(bRelease)

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("sequential batch did not settle")
	}
	if tracker.peakValue() != 1 {
		t.Fatalf("a Sequential Tool ran alongside others: peak = %d", tracker.peakValue())
	}
}

type panicSpecToolSet struct{}

func (panicSpecToolSet) Spec() ToolSetSpec { panic("broken Spec") }

func (panicSpecToolSet) Tools(context.Context) ([]Tool, error) { return nil, nil }

func TestToolSetSpecPanicBecomesRuntimeError(t *testing.T) {
	model := &recordingModel{scripts: [][]ModelEvent{finalScript("x")}}
	_, err := NewSession(WithModel(model), WithActiveToolSet(panicSpecToolSet{}))
	if err == nil {
		t.Fatal("a panicking ToolSet.Spec did not fail NewSession")
	}
	if !IsCode(err, ErrExtensionFailure) {
		t.Fatalf("ToolSet.Spec panic = %v, want extension_failure", err)
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
	agent, err := NewSession(WithModel(model), WithTools(tools...), WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
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
