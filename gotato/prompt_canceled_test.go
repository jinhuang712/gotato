package gotato

import (
	"context"
	"testing"
)

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
