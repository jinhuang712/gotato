package gotato

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type stalledTool struct {
	blocked <-chan struct{}
}

func (t stalledTool) Spec() ToolSpec {
	return ToolSpec{ID: "slow", Name: "slow", Description: "stalls", InputSchema: []byte(`{"type":"object"}`)}
}

func (t stalledTool) Execute(context.Context, ToolUse, ToolProgressFunc) (ToolResult, error) {
	<-t.blocked
	return ToolResult{Status: ToolResultOK}, nil
}

// A Tool that ignores its context must not pin the Session goroutine: Abort has
// to settle the Run even though the worker never returns.
func TestAbortSettlesRunWithStalledTool(t *testing.T) {
	blocked := make(chan struct{})
	defer close(blocked)
	call := ToolCall{ID: "c1", ToolID: "slow", Arguments: []byte(`{}`)}
	model := &testModel{scripts: [][]ModelEvent{
		{{Kind: ModelToolCall, ToolCall: &call}, {Kind: ModelDone, StopReason: StopToolCalls}},
		{{Kind: ModelTextDelta, Text: "done"}, {Kind: ModelDone, StopReason: StopEndTurn}},
	}}
	agent, err := NewSession(WithModel(model), WithTools(stalledTool{blocked: blocked}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	events, err := agent.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = events.Close() }()

	resultCh := make(chan error, 1)
	go func() {
		_, promptErr := agent.Prompt(context.Background(), UserMessage("go"))
		resultCh <- promptErr
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		event, nextErr := events.Next(ctx)
		if nextErr != nil {
			t.Fatalf("waiting for the Tool to start: %v", nextErr)
		}
		if event.Kind() == KindToolStarted {
			break
		}
	}

	agent.Abort()
	select {
	case promptErr := <-resultCh:
		if !IsCode(promptErr, ErrCancelled) {
			t.Fatalf("prompt error = %v", promptErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not settle after Abort with a stalled Tool")
	}
}

// concurrentProgressTool reports progress from several goroutines at once.
type concurrentProgressTool struct {
	workers int
	updates int
}

func (t *concurrentProgressTool) Spec() ToolSpec {
	return ToolSpec{ID: "progress", Name: "progress", InputSchema: []byte(`{"type":"object"}`)}
}

func (t *concurrentProgressTool) Execute(_ context.Context, _ ToolUse, progress ToolProgressFunc) (ToolResult, error) {
	var wg sync.WaitGroup
	for i := 0; i < t.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < t.updates; j++ {
				progress("x")
			}
		}()
	}
	wg.Wait()
	return ToolResult{Status: ToolResultOK}, nil
}

// A Tool may report progress from any goroutine. Every report under the limit
// must be counted exactly once; with -race this also exercises the counter and
// byte-limit guards under concurrent senders.
func TestConcurrentProgressReportsAreCounted(t *testing.T) {
	const workers, perWorker = 8, 8
	want := workers * perWorker
	call := ToolCall{ID: "c1", ToolID: "progress", Arguments: []byte(`{}`)}
	model := &testModel{scripts: [][]ModelEvent{
		{{Kind: ModelToolCall, ToolCall: &call}, {Kind: ModelDone, StopReason: StopToolCalls}},
		{{Kind: ModelTextDelta, Text: "done"}, {Kind: ModelDone, StopReason: StopEndTurn}},
	}}
	agent, err := NewSession(WithModel(model), WithTools(&concurrentProgressTool{workers: workers, updates: perWorker}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	events, err := agent.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = events.Close() }()

	updates := 0
	observed := make(chan error, 1)
	go func() {
		for {
			event, nextErr := events.Next(context.Background())
			if nextErr != nil {
				observed <- nextErr
				return
			}
			if event.Kind() == KindToolProgress {
				updates++
			}
			if event.Kind() == KindRunEnded {
				observed <- nil
				return
			}
		}
	}()

	if _, err := agent.Prompt(context.Background(), UserMessage("go")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-observed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("event stream did not reach run_ended")
	}
	if updates != want {
		t.Fatalf("progress updates = %d, want %d", updates, want)
	}
}

// hookTool runs hook, if any, and succeeds.
type hookTool struct {
	id   string
	hook func()
}

func (t hookTool) Spec() ToolSpec {
	return ToolSpec{ID: t.id, InputSchema: []byte(`{"type":"object"}`)}
}

func (t hookTool) Execute(context.Context, ToolUse, ToolProgressFunc) (ToolResult, error) {
	if t.hook != nil {
		t.hook()
	}
	return ToolResult{Content: []ContentPart{{Kind: ContentText, Text: "ok"}}}, nil
}

// callsScript is a Model reply that calls each Tool in order, as c1, c2, ….
func callsScript(toolIDs ...string) []ModelEvent {
	events := make([]ModelEvent, 0, len(toolIDs)+1)
	for i, id := range toolIDs {
		call := ToolCall{ID: ToolCallID("c" + string(rune('1'+i))), ToolID: id, Arguments: []byte(`{}`)}
		events = append(events, ModelEvent{Kind: ModelToolCall, ToolCall: &call})
	}
	return append(events, ModelEvent{Kind: ModelDone, StopReason: StopToolCalls})
}

func resultsByCall(messages []Message) map[ToolCallID]ToolResult {
	out := map[ToolCallID]ToolResult{}
	for _, message := range messages {
		if message.ToolResult != nil {
			out[message.ToolResult.CallID] = *message.ToolResult
		}
	}
	return out
}

// countCall counts the Events of one kind that name a Tool call.
func countCall(events []Event, kind EventKind, call ToolCallID) int {
	n := 0
	for _, event := range events {
		if event.Kind() != kind {
			continue
		}
		switch e := event.(type) {
		case ToolStarted:
			if e.CallID == call {
				n++
			}
		case ToolEnded:
			if e.CallID == call {
				n++
			}
		}
	}
	return n
}

// A Run aborted during one Tool of a sequential batch must not start the
// Tools after it: a Tool that ignores its context would carry out its side
// effect after the user stopped the Run. Before the fix nearly every Run
// launched them anyway and recorded them as unexecuted.
func TestAbortDoesNotLaunchLaterTools(t *testing.T) {
	for attempt := range 20 {
		var agent *Session
		launched := 0
		var mu sync.Mutex
		abort := hookTool{id: "abort", hook: func() { agent.Abort() }}
		count := hookTool{id: "later", hook: func() { mu.Lock(); launched++; mu.Unlock() }}
		model := &testModel{scripts: [][]ModelEvent{
			callsScript("abort", "later", "later"),
			{{Kind: ModelTextDelta, Text: "done"}, {Kind: ModelDone, StopReason: StopEndTurn}},
		}}
		var err error
		agent, err = NewSession(WithModel(model), WithTools(abort, count))
		if err != nil {
			t.Fatal(err)
		}
		log := recordAll(t, agent)
		if _, err := agent.Prompt(context.Background(), UserMessage("go")); !IsCode(err, ErrCancelled) {
			t.Fatalf("aborted Run error = %v", err)
		}
		_ = agent.Close(context.Background())
		mu.Lock()
		got := launched
		mu.Unlock()
		if got != 0 {
			t.Fatalf("attempt %d: %d Tools ran after Abort", attempt, got)
		}
		results := resultsByCall(agent.Transcript().Messages())
		for _, id := range []ToolCallID{"c2", "c3"} {
			result, ok := results[id]
			if !ok || result.Status != ToolResultCanceled || result.Executed {
				t.Fatalf("attempt %d: result of %s = %+v (found %v)", attempt, id, result, ok)
			}
			if starts, ends := countCall(log.events(), KindToolStarted, id), countCall(log.events(), KindToolEnded, id); starts != 1 || ends != 1 {
				t.Fatalf("attempt %d: %s has %d starts and %d ends", attempt, id, starts, ends)
			}
		}
	}
}

// A Tool whose executor started before the Run was aborted may have carried out
// its side effect, so its result must not claim it never ran. Before the fix a
// Tool still running at Abort was recorded with Executed false.
func TestToolStartedBeforeAbortIsRecordedExecuted(t *testing.T) {
	blocked := make(chan struct{})
	defer close(blocked)
	model := &testModel{scripts: [][]ModelEvent{
		callsScript("slow"),
		{{Kind: ModelTextDelta, Text: "done"}, {Kind: ModelDone, StopReason: StopEndTurn}},
	}}
	agent, err := NewSession(WithModel(model), WithTools(stalledTool{blocked: blocked}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	log := recordAll(t, agent)
	done := make(chan error, 1)
	go func() {
		_, promptErr := agent.Prompt(context.Background(), UserMessage("go"))
		done <- promptErr
	}()
	deadline := time.Now().Add(10 * time.Second)
	for countCall(log.events(), KindToolStarted, "c1") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the Tool did not start")
		}
		time.Sleep(time.Millisecond)
	}
	agent.Abort()
	if err := <-done; !IsCode(err, ErrCancelled) {
		t.Fatalf("aborted Run error = %v", err)
	}
	result, ok := resultsByCall(agent.Transcript().Messages())["c1"]
	if !ok || result.Status != ToolResultCanceled || !result.Executed {
		t.Fatalf("result of the started Tool = %+v (found %v)", result, ok)
	}
}

// failingPre is a PreToolUse Extension that fails on one call.
type failingPre struct{ failOn ToolCallID }

func (p *failingPre) Before(_ context.Context, use ToolUse) (PreToolDecision, error) {
	if use.CallID == p.failOn {
		return PreToolDecision{}, errors.New("pre hook failed")
	}
	return PreToolDecision{}, nil
}

// failingPost is a PostToolUse Extension that fails.
type failingPost struct{}

func (failingPost) After(context.Context, ToolResult) (ToolResult, error) {
	return ToolResult{}, errors.New("post hook failed")
}

// A Run that fails after its assistant Message committed Tool calls must still
// commit a result for every call. Every provider refuses a request in which a
// Tool call has no result, so before the fix every later Run of the Session
// failed.
func TestFailedBatchClosesOpenToolCalls(t *testing.T) {
	t.Run("extension in preflight", func(t *testing.T) {
		model := &testModel{scripts: [][]ModelEvent{
			callsScript("echo", "echo"),
			{{Kind: ModelTextDelta, Text: "done"}, {Kind: ModelDone, StopReason: StopEndTurn}},
		}}
		agent, err := NewSession(WithModel(model), WithTools(hookTool{id: "echo"}), WithExtension(&failingPre{failOn: "c2"}))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = agent.Close(context.Background()) }()
		if _, err := agent.Prompt(context.Background(), UserMessage("go")); !IsCode(err, ErrExtensionFailure) {
			t.Fatalf("first Run error = %v", err)
		}
		results := resultsByCall(agent.Transcript().Messages())
		for _, id := range []ToolCallID{"c1", "c2"} {
			result, ok := results[id]
			if !ok || result.Status != ToolResultFailed || result.Executed || !strings.Contains(result.SafeError, string(ErrExtensionFailure)) {
				t.Fatalf("closing result of %s = %+v (found %v)", id, result, ok)
			}
		}
		if _, err := agent.Prompt(context.Background(), UserMessage("again")); err != nil {
			t.Fatalf("second Run error = %v", err)
		}
		model.mu.Lock()
		request := model.last
		model.mu.Unlock()
		answered := map[ToolCallID]bool{}
		for _, message := range request.Messages {
			if message.ToolResult != nil {
				answered[message.ToolResult.CallID] = true
			}
		}
		for _, message := range request.Messages {
			for _, call := range message.ToolCalls {
				if !answered[call.ID] {
					t.Fatalf("the next request has Tool call %s without a result: %+v", call.ID, request.Messages)
				}
			}
		}
	})
	t.Run("extension after the Tool ran", func(t *testing.T) {
		model := &testModel{scripts: [][]ModelEvent{callsScript("echo")}}
		agent, err := NewSession(WithModel(model), WithTools(hookTool{id: "echo"}), WithExtension(failingPost{}))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = agent.Close(context.Background()) }()
		if _, err := agent.Prompt(context.Background(), UserMessage("go")); !IsCode(err, ErrExtensionFailure) {
			t.Fatalf("Run error = %v", err)
		}
		result, ok := resultsByCall(agent.Transcript().Messages())["c1"]
		if !ok || result.Status != ToolResultFailed || !result.Executed {
			t.Fatalf("closing result of the Tool that ran = %+v (found %v)", result, ok)
		}
		if err := validateContinuable(agent.Transcript().Messages()); err != nil {
			t.Fatalf("the Transcript does not end where a Model can continue: %v", err)
		}
	})
}
