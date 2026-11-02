package record_test

import (
	"context"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/record"
	"github.com/jinhuang712/gotato/store"
)

// After a load, the Messages of the Context that stand for a closed Segment
// must not share their Parts with the Segment, which is immutable.
func TestLoadedContextDoesNotAliasClosedSegments(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	writer, err := store.CreateWriter(ctx, st, record.New(record.WithID("alias")), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	rec := writer.Record()
	for _, id := range []string{"u1", "a1"} {
		message := gotato.UserMessage("text " + id)
		message.ID = gotato.MessageID(id)
		if err := rec.Append(message); err != nil {
			t.Fatal(err)
		}
	}
	segment := gotato.Segment{
		From: time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC), To: time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC),
		Messages: []gotato.Message{gotato.UserMessage("rendered")}, Recap: "recap",
	}
	closed, err := rec.CloseWithoutCheckpoint(segment)
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.CheckpointSegments(gotato.CompactionWhyPastCeiling, []int{closed.Number}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(ctx, st, "alias")
	if err != nil {
		t.Fatal(err)
	}
	segmentText, ok := record.ChangeContextInPlace(reopened, closed.Number)
	if !ok {
		t.Fatal("the reopened Record lost its Segment")
	}
	if segmentText != "rendered" {
		t.Fatalf("the closed Segment reads %q after the Context changed in place", segmentText)
	}
}
