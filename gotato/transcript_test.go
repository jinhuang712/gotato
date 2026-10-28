package gotato

import (
	"context"
	"sync"
	"testing"
)

func TestSessionCommitsToExternalTranscript(t *testing.T) {
	transcript := &recordingTranscript{}
	model := &testModel{scripts: [][]ModelEvent{{{Kind: ModelEventKindTextDelta, Text: "hello"}, {Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}}}}
	agent, err := NewSession(context.Background(), WithModel(model), WithTranscript(transcript))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	if _, err := agent.Prompt(context.Background(), UserMessage("hi")); err != nil {
		t.Fatal(err)
	}
	messages := transcript.Messages()
	if len(messages) != 2 || messages[0].Role != RoleUser || messages[1].Role != RoleAssistant {
		t.Fatalf("transcript = %+v", messages)
	}
	if transcript.appends != 2 {
		t.Fatalf("appends = %d", transcript.appends)
	}
	// A second Session against the same Transcript continues the history.
	second, err := NewSession(context.Background(), WithModel(model), WithTranscript(transcript))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close(context.Background()) }()
	if _, err := second.Prompt(context.Background(), UserMessage("again")); err != nil {
		t.Fatal(err)
	}
	if got := len(transcript.Messages()); got != 4 {
		t.Fatalf("transcript length after second agent = %d", got)
	}
	request := model.lastRequest()
	if len(request.Messages) != 3 {
		t.Fatalf("second agent saw %d messages, want 3 (history + new prompt)", len(request.Messages))
	}
}

// A caller may poll Session.Transcript while a Run appends to it.
func TestSessionTranscriptIsSafeToReadDuringARun(t *testing.T) {
	session, err := NewSession(context.Background(), WithModel(echoToolModel("call-1")), WithTools(&scriptedTool{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = session.Transcript().Len()
				_ = session.Transcript().Messages()
			}
		}
	}()
	if _, err := session.Prompt(context.Background(), UserMessage("go")); err != nil {
		t.Fatal(err)
	}
	close(stop)
	wg.Wait()
	// The slice a caller gets is its own.
	messages := session.Transcript().Messages()
	messages[0].ID = "mutated"
	if session.Transcript().Messages()[0].ID == "mutated" {
		t.Fatal("Messages returned the live slice")
	}
}

// Prompt with a context that has ended refuses before it commits anything:
// the error says NotRun and the Transcript is untouched, every time.
func TestPromptWithCanceledContextCommitsNothing(t *testing.T) {
	model := &testModel{scripts: [][]ModelEvent{{{Kind: ModelEventKindTextDelta, Text: "hi"}, {Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}}}}
	session, err := NewSession(context.Background(), WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := range 200 {
		_, err := session.Prompt(ctx, UserMessage("hello"))
		if !IsCode(err, CodeCanceled) || !NotRun(err) {
			t.Fatalf("call %d: err = %v, want canceled with NotRun", i, err)
		}
		if n := session.Transcript().Len(); n != 0 {
			t.Fatalf("call %d: %d Messages committed", i, n)
		}
	}
	if _, err := NewSession(ctx, WithModel(model)); !NotRun(err) {
		t.Fatalf("NewSession with an ended ctx: NotRun = false (%v)", err)
	}
}
