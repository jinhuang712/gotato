package session_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

func TestTakeoverEndsAnInterruptedRun(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	first, err := session.CreateWriter(ctx, store, session.New(), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Write(ctx, []gotato.Event{gotato.RunStarted{Header: gotato.NewHeader("", "run-1", 0)}}); err != nil {
		t.Fatal(err)
	}
	commit(t, first, gotato.UserMessage("hi"))
	// instance-a stops here, and never ends its Run.
	id := first.Record().ID()
	if _, err := session.OpenWriter(ctx, store, id, "instance-b"); err != nil {
		t.Fatal(err)
	}
	view, err := session.View(ctx, store, id)
	if err != nil {
		t.Fatal(err)
	}
	runs := view.Runs()
	if len(runs) != 1 || runs[0].Status != gotato.RunStatusFailed || runs[0].Error != "interrupted" {
		t.Fatalf("runs = %+v, want run-1 ended as interrupted", runs)
	}
	list, err := store.List(ctx)
	if err != nil || len(list) != 1 || list[0].Runs != 1 || list[0].Running != "" {
		t.Fatalf("summary = %+v, %v; want one Run, none running", list, err)
	}
}

// toolCallReply is an assistant reply that calls the demo tool twice.
func toolCallReply() gotato.Message {
	return gotato.Message{ID: "reply-1", Role: gotato.RoleAssistant, StopReason: gotato.StopReasonToolCalls, ToolCalls: []gotato.ToolCall{
		{ID: "call-1", ToolID: "demo.echo", Arguments: []byte(`{}`)},
		{ID: "call-2", ToolID: "demo.echo", Arguments: []byte(`{}`)},
	}}
}

func TestOpeningAnswersToolCallsLeftWithoutResult(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	writer, err := session.CreateWriter(ctx, store, session.New(), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	commit(t, writer, gotato.UserMessage("go"))
	commit(t, writer, toolCallReply())
	// call-1 started, then the instance stopped: call-2 never started.
	if _, err := writer.Write(ctx, []gotato.Event{gotato.ToolStarted{Header: header(), MessageID: "reply-1", CallID: "call-1", ToolID: "demo.echo"}}); err != nil {
		t.Fatal(err)
	}
	for _, open := range []func() (*session.Record, error){
		func() (*session.Record, error) { return session.Open(ctx, store, writer.ID()) },
		func() (*session.Record, error) { return session.View(ctx, store, writer.ID()) },
	} {
		r, err := open()
		if err != nil {
			t.Fatal(err)
		}
		messages := r.Messages()
		if len(messages) != 4 {
			t.Fatalf("messages = %+v, want both calls answered", messages)
		}
		stopped, notRun := messages[2].ToolResult, messages[3].ToolResult
		if stopped == nil || stopped.CallID != "call-1" || !stopped.Executed || stopped.Status != gotato.ToolResultStatusFailed || stopped.SafeError != session.StoppedToolResult {
			t.Fatalf("call-1 answered with %+v", stopped)
		}
		if notRun == nil || notRun.CallID != "call-2" || notRun.Executed || notRun.SafeError != session.NotRunToolResult {
			t.Fatalf("call-2 answered with %+v", notRun)
		}
	}
	// Nothing was written for the answers: the store still holds the facts.
	loaded, err := store.Load(ctx, writer.ID())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range loaded.Facts {
		if entry.Kind == gotato.EventKindToolResultCommitted && strings.Contains(string(entry.Data), "repair-") {
			t.Fatalf("a repair was written: %s", entry.Data)
		}
	}
}
