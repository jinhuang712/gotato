package gotato

import (
	"context"
	"testing"
	"time"
)

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
