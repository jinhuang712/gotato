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
		return ModelEvent{Kind: ModelEventKindTextDelta, Text: "partial"}, nil
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
	session, err := NewSession(context.Background(), WithModel(model))
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
	if err := session.Interrupt(context.Background(), UserMessage("do B instead")); err != nil {
		t.Fatal(err)
	}
	var result RunResult
	select {
	case result = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the interrupted Run did not settle")
	}
	if result.Status != RunStatusCompleted || TextOf(*result.FinalMessage) != "B done" {
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
			if e.Source != MessageSourceInterrupt {
				t.Fatalf("queued source = %q", e.Source)
			}
		case MessageCommitted:
			if e.Source == MessageSourceInterrupt {
				kinds = append(kinds, e.Kind())
			}
		case ReplyCutOff:
			if TextOf(e.Message) != "partial" {
				t.Fatalf("cut-off reply = %q", TextOf(e.Message))
			}
			kinds = append(kinds, e.Kind())
		}
	}
	if len(kinds) != 2 || kinds[0] != EventKindReplyCutOff || kinds[1] != EventKindMessageCommitted {
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
	return ToolResult{Content: []ContentPart{{Kind: ContentKindText, Text: "tool-ok"}}}, nil
}

func TestInterruptLetsARunningToolFinish(t *testing.T) {
	tool := &blockingTool{started: make(chan struct{}), release: make(chan struct{})}
	model := &recordingModel{scripts: [][]ModelEvent{toolCallScript(), finalScript("done")}}
	session, err := NewSession(context.Background(), WithModel(model), WithTools(tool))
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
	if err := session.Interrupt(context.Background(), UserMessage("change of plans")); err != nil {
		t.Fatal(err)
	}
	close(tool.release)
	select {
	case result := <-done:
		if result.Status != RunStatusCompleted {
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
	session, err := NewSession(context.Background(), WithModel(&recordingModel{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	if err := session.Interrupt(context.Background(), UserMessage("anyone there?")); !IsCode(err, CodeInvalidState) {
		t.Fatalf("Interrupt with no Run = %v", err)
	}
}

// An Abort that arrives once the Run has begun to write its end is not an
// abort_requested after run_ended: nothing is stored for it, and the Run's last
// write is the end of the Run.
func TestAbortAfterTheRunBeganToEndStoresNothing(t *testing.T) {
	journal := &journalTranscript{}
	s, err := NewSession(context.Background(), WithModel(&testModel{scripts: [][]ModelEvent{{{Kind: ModelEventKindTextDelta, Text: "hi"}, {Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}}}}), WithTranscript(journal))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close(context.Background()) }()
	s.endingHook = s.Abort
	if _, err := s.Prompt(context.Background(), UserMessage("go")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // an abort write in flight would land by now
	kinds := journal.kinds()
	for _, kind := range kinds {
		if kind == EventKindAbortRequested {
			t.Fatalf("an abort_requested was stored for an Abort that came after the Run began to end: %v", kinds)
		}
	}
	if kinds[len(kinds)-1] != EventKindRunEnded {
		t.Fatalf("the last Event is %s, want run_ended: %v", kinds[len(kinds)-1], kinds)
	}
}

// stalledJournal never finishes a write of a queued message until its ctx ends.
type stalledJournal struct{ journalTranscript }

func (j *stalledJournal) Write(ctx context.Context, events []Event) ([]Event, error) {
	for _, event := range events {
		if event.Kind() == EventKindMessageQueued {
			<-ctx.Done()
			return nil, ctx.Err()
		}
	}
	return j.journalTranscript.Write(ctx, events)
}

// Steer, FollowUp and Interrupt honor their ctx in the store write: a stalled
// store does not hold the caller past its deadline.
func TestSteerHonorsItsContext(t *testing.T) {
	session, err := NewSession(context.Background(), WithModel(&testModel{}), WithTranscript(&stalledJournal{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()

	ended, cancel := context.WithCancel(context.Background())
	cancel()
	if err := session.Steer(ended, UserMessage("late")); !IsCode(err, CodeCanceled) || !NotRun(err) {
		t.Fatalf("Steer with an ended ctx = %v, want canceled, NotRun", err)
	}

	ctx, cancelTimeout := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelTimeout()
	started := time.Now()
	err = session.FollowUp(ctx, UserMessage("stalled"))
	if !IsCode(err, CodeDeadlineExceeded) || time.Since(started) > 2*time.Second {
		t.Fatalf("FollowUp over a stalled store = %v after %v, want deadline_exceeded", err, time.Since(started))
	}
}

// runSpanKinds lists an Event stream as kind@run, so a test reads where each
// Event fell.
func runSpanKinds(events []Event) []string {
	var out []string
	for _, event := range events {
		out = append(out, string(event.Kind())+"@"+string(event.Head().RunID))
	}
	return out
}

// firstOfKind is the position of the first Event of kind, or -1.
func firstOfKind(events []Event, kind EventKind) int {
	for i, event := range events {
		if event.Kind() == kind {
			return i
		}
	}
	return -1
}

// A Steer from the run-start hook comes before the Run's run_started is
// written: it is stored outside the Run, with no RunID, not under the Run's.
func TestSteerBeforeRunStartedHasNoRun(t *testing.T) {
	var s *Session
	s, err := NewSession(context.Background(), WithModel(&recordingModel{scripts: [][]ModelEvent{finalScript("a"), finalScript("b")}}),
		WithRunStart(func(RunID) { _ = s.Steer(context.Background(), UserMessage("s")) }))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close(context.Background()) }()
	rec := recordAll(t, s)
	if _, err := s.Prompt(context.Background(), UserMessage("go")); err != nil {
		t.Fatal(err)
	}
	events := rec.events()
	queued := firstOfKind(events, EventKindMessageQueued)
	if queued < 0 {
		t.Fatalf("no message_queued: %v", runSpanKinds(events))
	}
	if run := events[queued].Head().RunID; run != "" {
		t.Errorf("message_queued before run_started names Run %s: %v", run, runSpanKinds(events))
	}
}

// A Notice from the run-start hook is stored outside the Run, like a Steer.
func TestNoticeBeforeRunStartedHasNoRun(t *testing.T) {
	var s *Session
	s, err := NewSession(context.Background(), WithModel(&recordingModel{scripts: [][]ModelEvent{finalScript("a")}}),
		WithRunStart(func(RunID) { _, _ = s.Notice(context.Background(), "k", "t") }))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close(context.Background()) }()
	rec := recordAll(t, s)
	if _, err := s.Prompt(context.Background(), UserMessage("go")); err != nil {
		t.Fatal(err)
	}
	events := rec.events()
	queued := firstOfKind(events, EventKindNoticeQueued)
	if queued < 0 {
		t.Fatalf("no notice_queued: %v", runSpanKinds(events))
	}
	if run := events[queued].Head().RunID; run != "" {
		t.Errorf("notice_queued before run_started names Run %s: %v", run, runSpanKinds(events))
	}
}

// A Steer whose write lands after the Run's run_ended does not name the Run.
func TestSteerAfterRunEndedHasNoRun(t *testing.T) {
	for range 200 {
		s, err := NewSession(context.Background(), WithModel(&recordingModel{scripts: [][]ModelEvent{finalScript("a")}}))
		if err != nil {
			t.Fatal(err)
		}
		rec := recordAll(t, s)
		done := make(chan struct{})
		s.endingHook = func() { go func() { _ = s.Steer(context.Background(), UserMessage("late")); close(done) }() }
		if _, err := s.Prompt(context.Background(), UserMessage("go")); err != nil {
			t.Fatal(err)
		}
		<-done
		rec.events() // requireRunSpans runs on it
		_ = s.Close(context.Background())
	}
}

// An Abort that comes before the Run's run_started is written is stored right
// after it, under the Run.
func TestAbortBeforeRunStartedIsStoredAfterIt(t *testing.T) {
	for range 300 {
		var s *Session
		s, err := NewSession(context.Background(), WithModel(&recordingModel{scripts: [][]ModelEvent{finalScript("a")}}), WithRunStart(func(RunID) { s.Abort() }))
		if err != nil {
			t.Fatal(err)
		}
		rec := recordAll(t, s)
		res, _ := s.Prompt(context.Background(), UserMessage("go"))
		events := rec.events()
		started, aborted, ended := firstOfKind(events, EventKindRunStarted), firstOfKind(events, EventKindAbortRequested), firstOfKind(events, EventKindRunEnded)
		if started < 0 || aborted < started || ended < aborted {
			t.Fatalf("want run_started, abort_requested, run_ended in order: %v", runSpanKinds(events))
		}
		if events[aborted].Head().RunID != res.RunID {
			t.Fatalf("abort_requested names Run %s, want %s", events[aborted].Head().RunID, res.RunID)
		}
		_ = s.Close(context.Background())
	}
}
