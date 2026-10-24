package modelctx_test

import (
	"context"
	"errors"
	"testing"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/session"
)

// segmentedRecord returns a Record whose Context starts with one Segment and
// has four more Messages after it.
func segmentedRecord(t *testing.T) *session.Record {
	t.Helper()
	record := session.New(session.WithID("segmented"))
	for _, text := range []string{"u1", "a1"} {
		m := gotato.UserMessage(text)
		if text == "a1" {
			m = gotato.AssistantMessage(text)
		}
		m.ID = gotato.MessageID("s-" + text)
		if err := record.Append(m); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := record.CloseSegment(gotato.Segment{Messages: record.Messages(), Recap: "recap"}); err != nil {
		t.Fatal(err)
	}
	if err := record.CheckpointSegments(gotato.CompactionOnDemand, []int{1}, nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, m := range toolConversation()[2:] {
		m.ID += "-t"
		if err := record.Append(m); err != nil {
			t.Fatal(err)
		}
	}
	return record
}

// Compact would flatten the Segments into one summary: it refuses, and the
// Record is left as it was.
func TestCompactRefusesARecordWithSegments(t *testing.T) {
	record := segmentedRecord(t)
	before := len(record.Messages())
	_, err := modelctx.Compact(context.Background(), record, modelctx.CompactOptions{Keep: 1})
	if !errors.Is(err, modelctx.ErrHasSegments) || !gotato.IsCode(err, gotato.ErrInvalidState) {
		t.Fatalf("Compact error = %v, want ErrHasSegments (invalid_state)", err)
	}
	if got := len(record.Messages()); got != before || len(record.Segments()) != 1 || len(record.Compactions()) != 0 {
		t.Fatalf("the refused Compact changed the Record: %d Messages, %d Segments", got, len(record.Segments()))
	}
}

// The automatic policy surfaces the same refusal to the Run that triggered it.
func TestAutoCompactorSurfacesTheRefusal(t *testing.T) {
	record := segmentedRecord(t)
	auto := modelctx.AutoCompact(record, modelctx.CompactPolicy{Ceiling: 1})
	if err := auto.PrepareRun(context.Background(), nil); !errors.Is(err, modelctx.ErrHasSegments) {
		t.Fatalf("PrepareRun error = %v, want ErrHasSegments", err)
	}
}
