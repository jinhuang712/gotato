package gotato

import (
	"context"
	"testing"
	"time"
)

// ctxJournal is a Journal that honors its ctx: a write under an ended ctx fails.
type ctxJournal struct{ journalTranscript }

func (j *ctxJournal) Write(ctx context.Context, events []Event) ([]Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return j.journalTranscript.Write(ctx, events)
}

// The Run's last write does not die with the caller's ctx: a Journal that
// honors its ctx still receives run_ended after a deadline.
func TestFinalWriteSurvivesCanceledContext(t *testing.T) {
	journal := &ctxJournal{}
	block := make(chan struct{})
	defer close(block)
	model := &testModel{scripts: [][]ModelEvent{{{Kind: ModelEventKindTextDelta, Text: "x"}}}, block: block}
	session, err := NewSession(context.Background(), WithModel(model), WithTranscript(journal))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	result, _ := session.Prompt(ctx, UserMessage("go"))
	if result.Status != RunStatusDeadlineExceeded {
		t.Fatalf("status = %q", result.Status)
	}
	kinds := journal.kinds()
	if len(kinds) == 0 || kinds[len(kinds)-1] != EventKindRunEnded {
		t.Fatalf("stored kinds = %v, want run_ended last", kinds)
	}
}
