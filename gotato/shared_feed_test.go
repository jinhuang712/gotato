package gotato

import (
	"context"
	"testing"
)

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
