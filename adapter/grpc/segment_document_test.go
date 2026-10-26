package grpcadapter

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	gotato "github.com/jinhuang712/gotato/gotato-core"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gotatov2 "github.com/jinhuang712/gotato/adapter/grpc/gotato/v2"
)

// A Session that holds Segments: GetSession carries the largest Segment number
// in the Document, and Compact finds nothing to close.
func TestSegmentedSessionOverGRPC(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	record := session.New(session.WithID("with-segments"))
	for _, m := range []gotato.Message{gotato.UserMessage("one"), gotato.AssistantMessage("two")} {
		m.ID = gotato.MessageID("m-" + gotato.TextOf(m))
		if err := record.Append(m); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := record.CloseSegmentAndCheckpoint(gotato.CompactionOnDemand, gotato.Segment{Messages: record.Messages(), Recap: "recap"}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	writer, err := session.Create(ctx, store, record, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Record().CloseSegment(gotato.Segment{Messages: []gotato.Message{gotato.UserMessage("lone")}}); err != nil { // listed by no Checkpoint
		t.Fatal(err)
	}
	if err := writer.Release(ctx); err != nil {
		t.Fatal(err)
	}
	runner, err := service.New(service.Config{Store: store, Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}}})
	if err != nil {
		t.Fatal(err)
	}
	client := clientFor(t, runner)

	got, err := client.GetSession(ctx, &gotatov2.SessionRequest{SessionId: "with-segments"})
	if err != nil {
		t.Fatal(err)
	}
	var doc session.Document
	if err := json.Unmarshal(got.GetDocumentJson(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.MaxSegment != 2 || len(doc.Segments) != 1 {
		t.Fatalf("Document: max_segment %d with %d Segments, want 2 and 1", doc.MaxSegment, len(doc.Segments))
	}

	// Nothing came since the Checkpoint, so there is nothing to close, and the
	// answer says so.
	compact, err := client.Compact(ctx, &gotatov2.CompactRequest{SessionId: "with-segments"})
	if err != nil || compact.GetReplaced() || compact.GetReason() != "nothing_to_close" {
		t.Fatalf("Compact = %v, %v", compact, err)
	}
}

// min_tokens skips a stretch estimated below it, and the answer says why.
func TestCompactTakesMinTokens(t *testing.T) {
	ctx := context.Background()
	runner, err := service.New(service.Config{Store: session.NewMemoryStore(), Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}}})
	if err != nil {
		t.Fatal(err)
	}
	var id string
	for _, prompt := range []string{"one", "two", "three"} {
		result, err := runner.Run(ctx, service.RunRequest{SessionID: id, Agent: "echo", Prompt: prompt})
		if err != nil {
			t.Fatal(err)
		}
		id = result.SessionID
	}
	client := clientFor(t, runner)
	skipped, err := client.Compact(ctx, &gotatov2.CompactRequest{SessionId: id, MinTokens: 100000})
	if err != nil || skipped.GetReplaced() || skipped.GetReason() != "below_min_tokens" || skipped.GetMessagesAfter() != 6 {
		t.Fatalf("below min_tokens = %v, %v", skipped, err)
	}
	if _, err := client.Compact(ctx, &gotatov2.CompactRequest{SessionId: id, MinTokens: -1}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("a negative min_tokens = %v, want InvalidArgument", err)
	}
	done, err := client.Compact(ctx, &gotatov2.CompactRequest{SessionId: id, MinTokens: 1})
	if err != nil || !done.GetReplaced() || done.GetReason() != "" || !strings.Contains(string(done.GetCompactionJson()), `"segment":1`) {
		t.Fatalf("compact = %v, %v", done, err)
	}
}

// A Session whose last Checkpoint holds one summary written before Segments is
// refused with FailedPrecondition.
func TestCompactRefusesASessionWithOneOldSummary(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	record := session.New(session.WithID("old-summary"))
	summary := gotato.UserMessage("Summary of earlier conversation:\nthey talked")
	summary.ID = "summary-1"
	summary.Parts[0].Metadata = map[string]string{"compaction": "summary"}
	record.Checkpoint(gotato.CompactionOnDemand, []gotato.Message{summary, gotato.UserMessage("one"), gotato.AssistantMessage("two")}, &session.Compaction{ReplacedMessages: 2, Recap: "truncate"})
	writer, err := session.Create(ctx, store, record, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Release(ctx); err != nil {
		t.Fatal(err)
	}
	runner, err := service.New(service.Config{Store: store, Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = clientFor(t, runner).Compact(ctx, &gotatov2.CompactRequest{SessionId: "old-summary", Keep: 1})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("Compact = %v, want FailedPrecondition", err)
	}
}
