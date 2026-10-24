package grpcadapter

import (
	"context"
	"encoding/json"
	"testing"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gotatov2 "github.com/jinhuang712/gotato/adapter/grpc/gotato/v2"
)

// A Session that holds Segments: GetSession carries the largest Segment number
// in the Document, and Compact refuses with FailedPrecondition.
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

	_, err = client.Compact(ctx, &gotatov2.CompactRequest{SessionId: "with-segments", Keep: 1})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("Compact = %v, want FailedPrecondition", err)
	}
}
