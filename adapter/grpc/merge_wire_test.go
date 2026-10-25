package grpcadapter

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"

	gotatov2 "github.com/jinhuang712/gotato/adapter/grpc/gotato/v2"
)

// A merged Segment and the merge's compaction record keep what they list on
// the wire: the Segments they stand for, and where each can be read.
func TestMergedSegmentOnTheWire(t *testing.T) {
	at := time.Date(2026, 10, 24, 10, 2, 0, 0, time.UTC)
	segment := gotato.Segment{
		Number: 7, From: at, To: at.Add(time.Hour), Recap: "## What happened\n- x",
		Merged: []gotato.MergedSegment{
			{Number: 1, From: at, To: at.Add(time.Minute), Source: gotato.SegmentSource{SessionID: "parent", FromNumber: 3, ToNumber: 40}},
			{Number: 2, From: at.Add(time.Minute), To: at.Add(time.Hour), Source: gotato.SegmentSource{FromNumber: 41, ToNumber: 60}},
		},
	}
	wire, err := eventOf(gotato.SegmentClosed{Header: gotato.Header{Time: at, SessionID: "s1", Number: 9}, Segment: segment})
	if err != nil {
		t.Fatal(err)
	}
	merged := wire.GetSegmentClosed().GetSegment().GetMerged()
	if len(merged) != 2 || merged[0].GetNumber() != 1 || merged[0].GetSource().GetSessionId() != "parent" || merged[0].GetSource().GetToNumber() != 40 ||
		merged[1].GetSource().GetFromNumber() != 41 || merged[1].GetFrom() == "" {
		t.Fatalf("merged on the wire = %v", merged)
	}
	ended, err := eventOf(gotato.CompactionEnded{
		Header: gotato.Header{Time: at}, Why: gotato.CompactionPastBudget,
		Compaction: &gotato.Compaction{Segment: 7, Merged: []int{1, 2}}, Segments: []int{7, 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := ended.GetCompactionEnded()
	if c.GetWhy() != "past_budget" || c.GetCompaction().GetSegment() != 7 || len(c.GetCompaction().GetMerged()) != 2 || c.GetCompaction().GetMerged()[1] != 2 {
		t.Fatalf("compaction_ended on the wire = %v", c)
	}
}

// Compact over gRPC says when it merged the oldest Segments, as JSON.
func TestCompactReportsTheMergeOverGRPC(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	runner, err := service.New(service.Config{Store: store, Agents: []service.Agent{{
		Name: "echo", Model: testkit.EchoModel{}, Compact: modelctx.CompactPolicy{SegmentBudget: 1},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	client := clientFor(t, runner)
	var id string
	compact := func() *gotatov2.CompactResult {
		t.Helper()
		result, err := client.Compact(withSessionID(ctx, id), &gotatov2.CompactRequest{SessionId: id})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	for _, prompt := range []string{"one", "two", "three"} {
		result, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: prompt})
		if err != nil {
			t.Fatal(err)
		}
		id = result.SessionID
	}
	if first := compact(); !first.GetReplaced() || len(first.GetMergeJson()) != 0 {
		t.Fatalf("the first compaction: %v", first)
	}
	for _, prompt := range []string{"four", "five"} {
		if _, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: prompt}); err != nil {
			t.Fatal(err)
		}
	}
	second := compact()
	var merge modelctx.MergeResult
	if err := json.Unmarshal(second.GetMergeJson(), &merge); err != nil {
		t.Fatalf("merge_json = %q: %v", second.GetMergeJson(), err)
	}
	if !second.GetReplaced() || !merge.Merged || len(merge.Replaced) != 2 || merge.Recap != "digest" {
		t.Fatalf("the second compaction: %v, merge %+v", second, merge)
	}
}
