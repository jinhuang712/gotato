package modelctx_test

import (
	"context"
	"errors"
	"testing"

	gotato "github.com/jinhuang712/gotato/gotato-core"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/session"
)

// oldSummaryRecord returns a Record whose last Checkpoint holds one summary
// written before Segments existed, with two Messages after it.
func oldSummaryRecord(t *testing.T) *session.Record {
	t.Helper()
	record := session.New(session.WithID("old-summary"))
	summary := gotato.UserMessage("Summary of earlier conversation:\nthey talked")
	summary.ID = "summary-1"
	summary.Parts[0].Metadata = map[string]string{modelctx.MetadataCompaction: "summary"}
	record.Checkpoint(gotato.CompactionOnDemand, []gotato.Message{summary, gotato.UserMessage("u2"), gotato.AssistantMessage("a2")}, &session.Compaction{ReplacedMessages: 4, Recap: "truncate"})
	return record
}

// A Record compacted into one summary before Segments is not migrated: Compact
// refuses it with invalid_state, and the Record is left as it was.
func TestCompactRefusesARecordWithOneOldSummary(t *testing.T) {
	record := oldSummaryRecord(t)
	before := len(record.Messages())
	_, err := modelctx.Compact(context.Background(), record, modelctx.CompactOptions{Keep: 1})
	if !errors.Is(err, modelctx.ErrOldSummary) || !gotato.IsCode(err, gotato.ErrInvalidState) {
		t.Fatalf("Compact error = %v, want ErrOldSummary (invalid_state)", err)
	}
	if got := len(record.Messages()); got != before || len(record.Segments()) != 0 || len(record.Compactions()) != 1 {
		t.Fatalf("the refused Compact changed the Record: %d Messages, %d Segments", got, len(record.Segments()))
	}
}

// The automatic policy surfaces the same refusal to the Run that triggered it.
func TestAutoCompactorSurfacesTheRefusal(t *testing.T) {
	record := oldSummaryRecord(t)
	auto := modelctx.AutoCompact(record, modelctx.CompactPolicy{Ceiling: 1})
	if _, err := auto.PrepareTurn(context.Background(), gotato.PendingTurn{RunID: "r", Turn: 1}); !errors.Is(err, modelctx.ErrOldSummary) {
		t.Fatalf("PrepareTurn error = %v, want ErrOldSummary", err)
	}
}
