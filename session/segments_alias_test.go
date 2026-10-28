package session

import (
	"context"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// After a load, the Messages of the Context that stand for a closed Segment
// must not share their Parts with the Segment, which is immutable.
func TestLoadedContextDoesNotAliasClosedSegments(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	writer, err := CreateWriter(ctx, store, New(WithID("alias")), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	record := writer.Record()
	for _, id := range []string{"u1", "a1"} {
		message := gotato.UserMessage("text " + id)
		message.ID = gotato.MessageID(id)
		if err := record.Append(message); err != nil {
			t.Fatal(err)
		}
	}
	segment := gotato.Segment{
		From: time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC), To: time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC),
		Messages: []gotato.Message{gotato.UserMessage("rendered")}, Recap: "recap",
	}
	closed, err := record.CloseSegment(segment)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.CheckpointSegments(gotato.CompactionWhyPastCeiling, []int{closed.Number}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, store, "alias")
	if err != nil {
		t.Fatal(err)
	}
	reopened.mu.Lock()
	defer reopened.mu.Unlock()
	if len(reopened.messages) == 0 || len(reopened.closed[closed.Number].Messages) == 0 {
		t.Fatal("the reopened Record lost its Segment")
	}
	reopened.messages[0].Parts[0].Text = "changed in place"
	if got := gotato.TextOf(reopened.closed[closed.Number].Messages[0]); got != "rendered" {
		t.Fatalf("the closed Segment reads %q after the Context changed in place", got)
	}
}
