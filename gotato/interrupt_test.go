package gotato

import (
	"context"
	"sync"
	"testing"
	"time"
)

// hangModel plays scripts like recordingModel, except that the first call
// streams "partial" and then waits for its context, so an Interrupt has a
// model call in flight to cut short.
type hangModel struct {
	mu       sync.Mutex
	requests [][]Message
	hanging  chan struct{}
	scripts  [][]ModelEvent
}

type hangStream struct {
	sent    bool
	hanging chan struct{}
}

func (s *hangStream) Recv(ctx context.Context) (ModelEvent, error) {
	if !s.sent {
		s.sent = true
		return ModelEvent{Kind: ModelTextDelta, Text: "partial"}, nil
	}
	close(s.hanging)
	<-ctx.Done()
	return ModelEvent{}, ctx.Err()
}

func (s *hangStream) Close() error { return nil }

func (m *hangModel) Stream(_ context.Context, request ModelRequest) (ModelStream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	index := len(m.requests)
	m.requests = append(m.requests, cloneMessages(request.Messages))
	if index == 0 && m.hanging != nil {
		return &hangStream{hanging: m.hanging}, nil
	}
	return &testStream{events: m.scripts[index-1]}, nil
}

func (m *hangModel) request(call int) []Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requests[call]
}

func roles(messages []Message) []string {
	out := make([]string, 0, len(messages))
	for _, message := range messages {
		out = append(out, string(message.Role)+":"+TextOf(message))
	}
	return out
}

func TestInterruptCutsTheModelCallShortAndStartsTheNextTurn(t *testing.T) {
	model := &hangModel{hanging: make(chan struct{}), scripts: [][]ModelEvent{finalScript("B done")}}
	session, err := NewSession(WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	seen := recordAll(t, session)

	done := make(chan RunResult, 1)
	go func() {
		result, runErr := session.Prompt(context.Background(), UserMessage("do A"))
		if runErr != nil {
			t.Error(runErr)
		}
		done <- result
	}()
	select {
	case <-model.hanging:
	case <-time.After(2 * time.Second):
		t.Fatal("the model call never started")
	}
	if err := session.Interrupt(UserMessage("do B instead")); err != nil {
		t.Fatal(err)
	}
	var result RunResult
	select {
	case result = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the interrupted Run did not settle")
	}
	if result.Status != RunCompleted || TextOf(*result.FinalMessage) != "B done" {
		t.Fatalf("result = %+v", result)
	}
	// The second call sees the user's words and the interrupt: a cut-off reply
	// stays in the Transcript but is left out of what the model reads.
	want := []string{"user:do A", "user:do B instead"}
	got := roles(model.request(1))
	if len(got) != len(want) {
		t.Fatalf("second request = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("second request = %v, want %v", got, want)
		}
	}
	var kinds []EventKind
	for _, event := range seen.events() {
		switch e := event.(type) {
		case MessageQueued:
			if e.Source != SourceInterrupt {
				t.Fatalf("queued source = %q", e.Source)
			}
		case MessageCommitted:
			if e.Source == SourceInterrupt {
				kinds = append(kinds, e.Kind())
			}
		case ReplyCutOff:
			if TextOf(e.Message) != "partial" {
				t.Fatalf("cut-off reply = %q", TextOf(e.Message))
			}
			kinds = append(kinds, e.Kind())
		}
	}
	if len(kinds) != 2 || kinds[0] != KindReplyCutOff || kinds[1] != KindMessageCommitted {
		t.Fatalf("events = %v", kinds)
	}
}

// blockingTool waits for release, and says whether its context was canceled.
type blockingTool struct {
	started  chan struct{}
	release  chan struct{}
	canceled bool
}

func (t *blockingTool) Spec() ToolSpec {
	return ToolSpec{ID: "demo.echo", InputSchema: []byte(`{"type":"object","required":["value"],"properties":{"value":{"type":"string"}},"additionalProperties":false}`)}
}

func (t *blockingTool) Execute(ctx context.Context, _ ToolUse, _ ToolProgressFunc) (ToolResult, error) {
	close(t.started)
	<-t.release
	t.canceled = ctx.Err() != nil
	return ToolResult{Content: []ContentPart{{Kind: ContentText, Text: "tool-ok"}}}, nil
}

func TestInterruptLetsARunningToolFinish(t *testing.T) {
	tool := &blockingTool{started: make(chan struct{}), release: make(chan struct{})}
	model := &recordingModel{scripts: [][]ModelEvent{toolCallScript(), finalScript("done")}}
	session, err := NewSession(WithModel(model), WithTools(tool))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()

	done := make(chan RunResult, 1)
	go func() {
		result, runErr := session.Prompt(context.Background(), UserMessage("use the tool"))
		if runErr != nil {
			t.Error(runErr)
		}
		done <- result
	}()
	select {
	case <-tool.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the tool never started")
	}
	if err := session.Interrupt(UserMessage("change of plans")); err != nil {
		t.Fatal(err)
	}
	close(tool.release)
	select {
	case result := <-done:
		if result.Status != RunCompleted {
			t.Fatalf("result = %+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the Run did not settle")
	}
	if tool.canceled {
		t.Fatal("an Interrupt canceled the running tool")
	}
	// The tool's result lands first, and the interrupt follows it.
	messages := model.transcript(1)
	if len(messages) < 4 || messages[2].Role != RoleToolResult || TextOf(messages[3]) != "change of plans" {
		t.Fatalf("second request = %v", roles(messages))
	}
}

func TestInterruptWithoutARunIsInvalidState(t *testing.T) {
	session, err := NewSession(WithModel(&recordingModel{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	if err := session.Interrupt(UserMessage("anyone there?")); !IsCode(err, ErrInvalidState) {
		t.Fatalf("Interrupt with no Run = %v", err)
	}
}
