package grpcadapter

import (
	"encoding/json"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato"
)

func TestSegmentClosedAndCheckpointOnTheWire(t *testing.T) {
	at := time.Date(2026, 10, 24, 10, 2, 0, 0, time.UTC)
	message := gotato.UserMessage("hello")
	message.ID, message.RunID = "m1", "run-7"
	segment := gotato.Segment{
		Number: 2, From: at, To: at.Add(time.Hour),
		Source:   gotato.SegmentSource{SessionID: "parent", FromNumber: 3, ToNumber: 40},
		Messages: []gotato.Message{message}, Rendered: 1,
		ToolCalls: []gotato.ToolCallLine{{At: at, ToolID: "logs.query", Args: "{}", Status: gotato.ToolResultFailed, Code: "timeout", Count: 2}},
		Recap:     "## What happened\n- x", Tools: []string{"logs.tail"},
	}
	wire, err := eventOf(gotato.SegmentClosed{Header: gotato.Header{Time: at, SessionID: "s1", Number: 9}, Segment: segment})
	if err != nil {
		t.Fatal(err)
	}
	if KindOf(wire) != string(gotato.KindSegmentClosed) {
		t.Fatalf("kind = %q, want segment_closed", KindOf(wire))
	}
	got := wire.GetSegmentClosed().GetSegment()
	var decoded gotato.Message
	if err := json.Unmarshal(got.GetMessagesJson()[0], &decoded); err != nil {
		t.Fatal(err)
	}
	if got.GetNumber() != 2 || got.GetSource().GetSessionId() != "parent" || got.GetSource().GetToNumber() != 40 || got.GetRendered() != 1 ||
		decoded.ID != "m1" || decoded.RunID != "run-7" || got.GetToolCalls()[0].GetCode() != "timeout" || got.GetToolCalls()[0].GetCount() != 2 || got.GetRecap() == "" || got.GetTools()[0] != "logs.tail" {
		t.Fatalf("segment on the wire = %v", got)
	}

	usage := gotato.Usage{InputTokens: 7}
	ended, err := eventOf(gotato.CompactionEnded{
		Header: gotato.Header{Time: at}, Why: gotato.CompactionOverflow,
		Compaction: &gotato.Compaction{Segment: 2, TokensBefore: 900, TokensAfter: 100, Ceiling: 800, RecapUsage: &usage, Tools: []string{"logs.tail"}},
		Segments:   []int{1, 2}, Tail: []gotato.MessagePosition{{ID: "m9", Number: 12, Time: at}}, Tools: []string{"logs.tail"},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := ended.GetCompactionEnded()
	if c.GetWhy() != "overflow" || c.GetCompaction().GetSegment() != 2 || c.GetCompaction().GetTokensBefore() != 900 || c.GetCompaction().GetCeiling() != 800 ||
		c.GetCompaction().GetRecapUsage().GetInputTokens() != 7 || len(c.GetSegments()) != 2 || c.GetTail()[0].GetNumber() != 12 || c.GetTools()[0] != "logs.tail" {
		t.Fatalf("compaction_ended on the wire = %v", c)
	}
	plain, err := eventOf(gotato.CompactionEnded{Header: gotato.Header{Time: at}, Compaction: &gotato.Compaction{}})
	if err != nil || plain.GetCompactionEnded().GetCompaction().GetRecapUsage() != nil {
		t.Fatalf("a compaction with no Recap call carries a RecapUsage: %v, %v", plain, err)
	}
}
