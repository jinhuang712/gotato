package grpcadapter

import (
	"context"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	gotatov2 "github.com/jinhuang712/gotato/adapter/grpc/gotato/v2"
)

// A notice is queued for the Run in flight and answered at once with the
// number of its notice_queued Event; it starts no Run, so the Run settles
// without it and it goes in after the prompt of the next one.
func TestNoticeIsQueuedAndReportedAsAnEvent(t *testing.T) {
	store, mesh := session.NewMemoryStore(), newPeers()
	g := newGate()
	a := newNode(t, store, mesh, "instance-a", g, false)
	id, wait := running(t, a, g)

	response, err := a.client.Notice(context.Background(), &gotatov2.NoticeRequest{SessionId: id, Kind: "order_update", Text: "Order 42 was delivered."})
	if err != nil || !response.GetQueued() || response.GetNumber() == 0 || response.GetMessageId() == "" || response.GetSessionId() != id {
		t.Fatalf("Notice = %v, %v; want queued with a number", response, err)
	}
	close(g.release)
	wait()
	if got := transcriptOf(t, a, id); got != "user:hello|assistant:reply to hello" {
		t.Fatalf("transcript = %q: a notice kept the Run going", got)
	}
	if _, err := a.runner.Run(context.Background(), service.RunRequest{SessionID: id, Prompt: "again"}); err != nil {
		t.Fatal(err)
	}
	if got := transcriptOf(t, a, id); !strings.Contains(got, "user:again|notice:Order 42 was delivered.|assistant:") {
		t.Fatalf("transcript = %q, want the notice after the prompt", got)
	}

	// Both steps are on the wire as Events of their own.
	stream, err := a.client.Events(context.Background(), &gotatov2.EventsRequest{SessionId: id})
	if err != nil {
		t.Fatal(err)
	}
	var queued *gotatov2.NoticeQueued
	var committed *gotatov2.NoticeCommitted
	var queuedNumber uint64
	for {
		event, err := stream.Recv()
		if err != nil {
			break
		}
		if n := event.GetNoticeQueued(); n != nil {
			queued, queuedNumber = n, event.GetHeader().GetNumber()
		}
		if n := event.GetNoticeCommitted(); n != nil {
			committed = n
		}
	}
	if queued == nil || committed == nil || queued.GetNoticeKind() != "order_update" || queued.GetSource() != "service" || queued.GetText() != "Order 42 was delivered." {
		t.Fatalf("notice_queued %v, notice_committed %v", queued, committed)
	}
	if queuedNumber != response.GetNumber() || committed.GetQueued() != response.GetNumber() || !strings.Contains(string(committed.GetMessageJson()), `"role":"notice"`) {
		t.Fatalf("numbers: queued event %d, answer %d, commit names %d (%s)", queuedNumber, response.GetNumber(), committed.GetQueued(), committed.GetMessageJson())
	}

	// A refused notice is refused before it queues.
	for name, request := range map[string]*gotatov2.NoticeRequest{
		"reserved": {SessionId: id, Kind: "time_away", Text: "x"},
		"no text":  {SessionId: id, Kind: "k"},
	} {
		if _, err := a.client.Notice(context.Background(), request); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("%s = %v, want INVALID_ARGUMENT", name, err)
		}
	}
	if _, err := a.client.Notice(context.Background(), &gotatov2.NoticeRequest{SessionId: "missing", Kind: "k", Text: "x"}); status.Code(err) != codes.NotFound {
		t.Fatalf("a notice for a missing Session = %v, want NOT_FOUND", err)
	}
}

func TestNoticeForARunElsewhereIsNotWriterOrIsForwarded(t *testing.T) {
	store, mesh := session.NewMemoryStore(), newPeers()
	g := newGate()
	a := newNode(t, store, mesh, "instance-a", g, false)
	plain := newNode(t, store, mesh, "instance-b", testkit.EchoModel{}, false)
	forwarding := newNode(t, store, mesh, "instance-c", testkit.EchoModel{}, true)
	id, wait := running(t, a, g)

	var trailer metadata.MD
	_, err := plain.client.Notice(context.Background(), &gotatov2.NoticeRequest{SessionId: id, Kind: "k", Text: "from b"}, grpc.Trailer(&trailer))
	if status.Code(err) != codes.FailedPrecondition || trailer.Get(NotRunKey)[0] != "true" || trailer.Get(RetryableKey)[0] != "true" {
		t.Fatalf("Notice = %v, trailer %v; want FAILED_PRECONDITION, not run, retryable", err, trailer)
	}
	if got := trailer.Get(WriterKey); len(got) != 1 || got[0] != "passthrough:///instance-a" {
		t.Fatalf("trailer = %v, want the writer's address", trailer)
	}
	before := a.calls.Load()
	response, err := forwarding.client.Notice(context.Background(), &gotatov2.NoticeRequest{SessionId: id, Kind: "k", Text: "via c"})
	if err != nil || !response.GetQueued() || a.calls.Load() != before+1 {
		t.Fatalf("forwarded Notice = %v, %v (%d calls at the writer)", response, err, a.calls.Load()-before)
	}
	close(g.release)
	wait()
}
