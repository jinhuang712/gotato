package gotato

import (
	"context"
	"sync"
	"testing"
)

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
