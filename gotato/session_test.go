package gotato

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func TestSessionPromptEventsAndClose(t *testing.T) {
	model := &testModel{scripts: [][]ModelEvent{{{Kind: ModelEventKindTextDelta, Text: "hello"}, {Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}}}}
	agent, err := NewSession(context.Background(), WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	stream, err := agent.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	result, err := agent.Prompt(context.Background(), UserMessage("hi"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != RunStatusCompleted || TextOf(*result.FinalMessage) != "hello" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Metrics.Turns != 1 || result.Metrics.TextBytes != uint64(len("hello")) {
		t.Fatalf("run metrics = %+v", result.Metrics)
	}
	if got := agent.Status(); got != SessionStatusIdle {
		t.Fatalf("status after Run = %s", got)
	}
	if _, err := agent.Prompt(context.Background(), UserMessage("again")); err != nil {
		t.Fatal(err)
	}
	if err := agent.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := agent.Status(); got != SessionStatusClosed {
		t.Fatalf("status after Close = %s", got)
	}
	if _, err := agent.Prompt(context.Background(), UserMessage("closed")); !IsCode(err, CodeSessionClosed) {
		t.Fatalf("expected closed error, got %v", err)
	}

	// Close ended the stream after the Events queued for it, so the drain is
	// deterministic. Each Run must end with exactly one RunEnded as its final
	// Event: a duplicate or late terminal Event must fail this test.
	var kinds []EventKind
	var turnEnded *TurnEnded
	terminals := map[RunID]int{}
	lastKind := map[RunID]EventKind{}
	for {
		event, nextErr := stream.Next(context.Background())
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		kinds = append(kinds, event.Kind())
		if run := event.Head().RunID; run != "" {
			lastKind[run] = event.Kind()
		}
		if ended, ok := event.(TurnEnded); ok && turnEnded == nil {
			turnEnded = &ended
		}
		if _, ok := event.(RunEnded); ok {
			terminals[event.Head().RunID]++
		}
	}
	if turnEnded == nil || turnEnded.ToolCalls != 0 || turnEnded.TextBytes != uint64(len("hello")) {
		t.Fatalf("unexpected TurnEnded = %#v", turnEnded)
	}
	if len(terminals) != 2 {
		t.Fatalf("Runs with a terminal Event = %d in %v", len(terminals), kinds)
	}
	for run, count := range terminals {
		if count != 1 {
			t.Fatalf("run_ended count = %d for run %s in %v", count, run, kinds)
		}
		if lastKind[run] != EventKindRunEnded {
			t.Fatalf("run %s ended with %s, want run_ended", run, lastKind[run])
		}
	}
	// The Session says it left: SessionUnloaded is the last Event of the stream.
	if len(kinds) == 0 || kinds[len(kinds)-1] != EventKindSessionUnloaded {
		t.Fatalf("final Event = %v", kinds)
	}
	replyStarted, textDelta := -1, -1
	for i, kind := range kinds {
		if kind == EventKindReplyStarted && replyStarted < 0 {
			replyStarted = i
		}
		if kind == EventKindTextDelta && textDelta < 0 {
			textDelta = i
		}
	}
	if replyStarted < 0 || textDelta < 0 || replyStarted > textDelta {
		t.Fatalf("reply event order = %v", kinds)
	}
	_ = stream.Close()
}

func TestSessionAbort(t *testing.T) {
	started := make(chan struct{}, 1)
	block := make(chan struct{})
	model := &testModel{started: started, block: block, scripts: [][]ModelEvent{{{Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}}}}
	agent, err := NewSession(context.Background(), WithModel(model))
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
		_, promptErr := agent.Prompt(context.Background(), UserMessage("cancel me"))
		resultCh <- promptErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("Model was not called")
	}
	for {
		event, nextErr := events.Next(context.Background())
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		if _, ok := event.(RunStarted); ok {
			break
		}
	}
	agent.Abort()
	select {
	case err := <-resultCh:
		if !IsCode(err, CodeCanceled) {
			t.Fatalf("prompt error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled Run did not return")
	}
}

func TestSessionSingleFlight(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	model := &testModel{started: started, block: release, scripts: [][]ModelEvent{{{Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}}}}
	agent, err := NewSession(context.Background(), WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	first := make(chan error, 1)
	go func() { _, runErr := agent.Prompt(context.Background(), UserMessage("first")); first <- runErr }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("Model was not called")
	}
	if _, err := agent.Prompt(context.Background(), UserMessage("second")); !IsCode(err, CodeBusy) {
		t.Fatalf("expected busy, got %v", err)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
}

func TestSessionCloseWhileBusyIsBounded(t *testing.T) {
	release := make(chan struct{})
	model := &testModel{block: release, scripts: [][]ModelEvent{{{Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}}}}
	agent, err := NewSession(context.Background(), WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	promptDone := make(chan error, 1)
	go func() { _, runErr := agent.Prompt(context.Background(), UserMessage("blocking")); promptDone <- runErr }()
	deadline := time.Now().Add(10 * time.Second)
	for agent.Status() != SessionStatusBusy {
		if time.Now().After(deadline) {
			t.Fatal("Session did not become Busy")
		}
		time.Sleep(time.Millisecond)
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := agent.Close(closeCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected bounded Close timeout, got %v", err)
	}
	if got := agent.Status(); got != SessionStatusClosing {
		t.Fatalf("status after timeout = %s", got)
	}
	close(release)
	if err := <-promptDone; err != nil {
		t.Fatal(err)
	}
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelWait()
	if err := agent.Close(waitCtx); err != nil {
		t.Fatal(err)
	}
	if got := agent.Status(); got != SessionStatusClosed {
		t.Fatalf("status after close = %s", got)
	}
}

// A Tool Call's Signature is the adapter's: the Session stores it with the
// call and sends it back on the next Turn unchanged.
func TestToolCallSignatureIsStoredAndReplayed(t *testing.T) {
	model := &testModel{scripts: [][]ModelEvent{
		{{Kind: ModelEventKindToolCall, ToolCall: &ToolCall{ID: "call-1", ToolID: "demo.echo", Arguments: []byte(`{"value":"x"}`), Signature: []byte("item-1")}}, {Kind: ModelEventKindDone, StopReason: StopReasonToolCalls}},
		{{Kind: ModelEventKindTextDelta, Text: "final"}, {Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}},
	}}
	transcript := &recordingTranscript{}
	agent, err := NewSession(context.Background(), WithModel(model), WithTools(&scriptedTool{}), WithTranscript(transcript))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	if _, err := agent.Prompt(context.Background(), UserMessage("use tool")); err != nil {
		t.Fatal(err)
	}
	messages := transcript.Messages()
	if len(messages) < 2 || len(messages[1].ToolCalls) != 1 {
		t.Fatalf("transcript = %+v", messages)
	}
	if stored := messages[1].ToolCalls[0]; stored.ID != "call-1" || string(stored.Signature) != "item-1" {
		t.Fatalf("stored call = %+v", stored)
	}
	request := model.lastRequest()
	if len(request.Messages) < 2 || len(request.Messages[1].ToolCalls) != 1 || string(request.Messages[1].ToolCalls[0].Signature) != "item-1" {
		t.Fatalf("replayed request = %+v", request.Messages)
	}
}

func TestSessionToolLoop(t *testing.T) {
	tool := &scriptedTool{}
	agent, err := NewSession(context.Background(), WithModel(&toolModel{}), WithTools(tool))
	if err != nil {
		t.Fatal(err)
	}
	result, err := agent.Prompt(context.Background(), UserMessage("use tool"))
	if err != nil {
		t.Fatal(err)
	}
	if TextOf(*result.FinalMessage) != "final" {
		t.Fatalf("unexpected final message: %+v", result.FinalMessage)
	}
	tool.mu.Lock()
	calls := tool.calls
	tool.mu.Unlock()
	if calls != 1 {
		t.Fatalf("tool calls = %d", calls)
	}
	if err := agent.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPromptRejectsBlankParts(t *testing.T) {
	model := &testModel{scripts: [][]ModelEvent{{{Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}}}}
	agent, err := NewSession(context.Background(), WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	for _, message := range []Message{UserMessage(""), UserMessage("   "), {Role: RoleUser}} {
		if _, err := agent.Prompt(context.Background(), message); !IsCode(err, CodeInvalidArgument) {
			t.Fatalf("Prompt(%+v) err = %v, want invalid_argument", message, err)
		}
	}
	image := Message{Role: RoleUser, Parts: []ContentPart{{Kind: ContentKindImage, Data: []byte{1, 2, 3}, MIMEType: "image/png"}}}
	if _, err := agent.Prompt(context.Background(), image); err != nil {
		t.Fatalf("image-only prompt rejected: %v", err)
	}
}

// A host's Feed is shared by the Sessions of one id. A Session over a Transcript
// that is not a Journal numbers its Events after the Feed's last number, so the
// Events are Events of the stream, not numbers that repeat it.
func TestSharedFeedOverAPlainTranscriptContinuesItsNumbers(t *testing.T) {
	feed := NewFeed(nil)
	for n := uint64(1); n <= 5; n++ {
		feed.Publish(storedEvent(n))
	}
	sub, err := feed.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()
	s, err := NewSession(context.Background(), WithModel(&testModel{scripts: [][]ModelEvent{{{Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}}}}), WithFeed(feed))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close(context.Background()) }()
	if _, err := s.Prompt(context.Background(), UserMessage("go")); err != nil {
		t.Fatal(err)
	}
	if first := next(t, sub); first.Head().Number != 6 {
		t.Fatalf("the Session's first Event is numbered %d, want 6, after the Feed's last", first.Head().Number)
	}
}

// A refused Continue gives the admission token back before it answers, so the
// caller that gets the error can start its next operation at once.
func TestRefusedContinueFreesAdmissionBeforeItAnswers(t *testing.T) {
	s, err := NewSession(context.Background(), WithModel(&recordingModel{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close(context.Background()) }()
	for i := 0; i < 2000; i++ {
		if _, err := s.Continue(context.Background()); err == nil {
			t.Fatal("Continue on an empty Session succeeded")
		}
		if _, err := s.InspectContext(context.Background()); IsCode(err, CodeBusy) {
			t.Fatalf("InspectContext after a refused Continue was busy (round %d)", i)
		}
	}
}
