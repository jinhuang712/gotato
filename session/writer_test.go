package session_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/session"
)

func TestStaleCopyCannotOverwriteANewerRecord(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	first, err := session.Create(ctx, store, session.New(), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	id := first.Record().ID()
	second, err := session.OpenWriter(ctx, store, id, "instance-b")
	if err != nil {
		t.Fatal(err)
	}
	if claim := second.Record().Claim(); claim.Instance != "instance-b" || claim.Epoch != 2 {
		t.Fatalf("claim = %+v, want instance-b at epoch 2", claim)
	}
	_ = second.Record().Append(gotato.UserMessage("from b"))
	if err := second.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	_ = first.Record().Append(gotato.UserMessage("from a, stale"))
	err = first.Flush(ctx)
	if !errors.Is(err, session.ErrConflict) || !gotato.IsCode(err, gotato.ErrConflict) {
		t.Fatalf("stale Flush = %v, want a conflict", err)
	}
	if again := first.Flush(ctx); !errors.Is(again, session.ErrConflict) {
		t.Fatalf("a Writer that failed wrote again: %v", again)
	}
	stored, err := session.Open(ctx, store, id)
	if err != nil {
		t.Fatal(err)
	}
	if messages := stored.Messages(); len(messages) != 1 || gotato.TextOf(messages[0]) != "from b" {
		t.Fatalf("stored = %+v, want only the newer writer's Message", messages)
	}
}

// TestReopeningByTheHolderWritesABusyMark: the Instance that holds the Claim
// writes no new Claim when it opens the Record again, but it does mark the
// Record busy in the store, at the next position, so another Instance that
// claims it at the same moment loses that position, or wins it.
func TestReopeningByTheHolderWritesABusyMark(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	created, err := session.Create(ctx, store, session.New(), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	id := created.Record().ID()
	before, err := store.Load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := session.OpenWriter(ctx, store, id, "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	if stats := reopened.Stats(); stats.Writes != 1 {
		t.Fatalf("reopening wrote %d times, want one busy mark", stats.Writes)
	}
	if claim := reopened.Record().Claim(); claim != (session.Claim{Instance: "instance-a", Epoch: 1}) {
		t.Fatalf("claim = %+v, want instance-a still at epoch 1", claim)
	}
	entries, err := store.Log(ctx, id, before.Head)
	if err != nil || len(entries) != 1 || entries[0].Kind != session.EntryBusy || entries[0].Seq != before.Head+1 {
		t.Fatalf("written after the head = %+v, %v; want one busy mark at %d", entries, err, before.Head+1)
	}
	after, err := store.Load(ctx, id)
	if err != nil || !after.Summary.Busy {
		t.Fatalf("summary = %+v, %v; want busy", after.Summary, err)
	}
	if err := reopened.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if released, err := store.Load(ctx, id); err != nil || released.Summary.Busy {
		t.Fatalf("summary after Release = %+v, %v; want idle", released.Summary, err)
	}
	if _, err := session.View(ctx, store, id); err != nil {
		t.Fatalf("a Record with a busy mark does not load: %v", err)
	}
}

func TestTakeoverEndsAnInterruptedRun(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	first, err := session.Create(ctx, store, session.New(), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	first.Record().RecordRunStart("run-1", time.Now())
	_ = first.Record().Append(gotato.UserMessage("hi"))
	if err := first.Flush(ctx); err != nil {
		t.Fatal(err)
	}
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
	if len(runs) != 1 || runs[0].Status != gotato.RunFailed || runs[0].Error != "interrupted" {
		t.Fatalf("runs = %+v, want run-1 ended as interrupted", runs)
	}
	list, err := store.List(ctx)
	if err != nil || len(list) != 1 || list[0].Runs != 1 || list[0].Running != "" {
		t.Fatalf("summary = %+v, %v; want one Run, none running", list, err)
	}
}

func TestFileStoreReadsVersion1Documents(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	doc := session.Document{
		SchemaVersion: 1, ID: "old", CreatedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC),
		Messages: []gotato.Message{gotato.UserMessage("hi"), gotato.AssistantMessage("hello")},
		Runs:     []session.Run{{RunID: "run-1", Status: gotato.RunCompleted}},
		Usage:    gotato.Usage{TotalTokens: 7},
		Metadata: map[string]string{"agent": "support"},
	}
	data, _ := json.Marshal(doc)
	if err := os.WriteFile(filepath.Join(dir, "old.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := session.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	list, err := store.List(ctx)
	if err != nil || len(list) != 1 || list[0].ID != "old" || list[0].Messages != 2 || list[0].Runs != 1 {
		t.Fatalf("List = %+v, %v", list, err)
	}
	view, err := session.View(ctx, store, "old")
	if err != nil || view.Len() != 2 || len(view.Runs()) != 1 || view.Usage().TotalTokens != 7 {
		t.Fatalf("View = %+v, %v", view, err)
	}
	writer, err := session.OpenWriter(ctx, store, "old", "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	_ = writer.Record().Append(gotato.UserMessage("again"))
	if err := writer.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "old.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the version 1 document stayed after the first write: %v", err)
	}
	reopened, err := session.Open(ctx, store, "old")
	if err != nil || reopened.Len() != 3 {
		t.Fatalf("reopened = %v, %v", reopened, err)
	}
	if value, _ := reopened.Get("agent"); value != "support" {
		t.Fatalf("metadata = %v", reopened.Metadata())
	}
}

func TestFileStoreIgnoresUnlinkedTempFiles(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := session.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := session.Create(ctx, store, session.New(session.WithID("r1")), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	// A crash between writing a temporary file and linking it leaves this.
	if err := os.WriteFile(filepath.Join(dir, "r1", ".tmp-12345"), []byte("{torn"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = writer.Record().Append(gotato.UserMessage("hi"))
	if err := writer.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	reopened, err := session.Open(ctx, store, "r1")
	if err != nil || reopened.Len() != 1 {
		t.Fatalf("Open = %v, %v", reopened, err)
	}
}

func TestCreateRefusesATakenID(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	if _, err := session.Create(ctx, store, session.New(session.WithID("taken")), "instance-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Create(ctx, store, session.New(session.WithID("taken")), "instance-b"); !errors.Is(err, session.ErrExists) {
		t.Fatalf("Create = %v, want ErrExists", err)
	}
}

// toolCallReply is an assistant reply that calls the demo tool twice.
func toolCallReply() gotato.Message {
	return gotato.Message{ID: "reply-1", Role: gotato.RoleAssistant, StopReason: gotato.StopToolCalls, ToolCalls: []gotato.ToolCall{
		{ID: "call-1", ToolID: "demo.echo", Arguments: []byte(`{}`)},
		{ID: "call-2", ToolID: "demo.echo", Arguments: []byte(`{}`)},
	}}
}

func TestOpeningAnswersToolCallsLeftWithoutResult(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	writer, err := session.Create(ctx, store, session.New(), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	_ = writer.Record().Append(gotato.UserMessage("go"))
	_ = writer.Record().Append(toolCallReply())
	// call-1 started, then the instance stopped: call-2 never started.
	if err := writer.Step(ctx, gotato.Step{Kind: gotato.StepToolStart, MessageID: "reply-1", Calls: []gotato.ToolCallID{"call-1"}}); err != nil {
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
		if stopped == nil || stopped.CallID != "call-1" || !stopped.Executed || stopped.Status != gotato.ToolResultFailed || stopped.SafeError != session.StoppedToolResult {
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
		if entry.Kind == session.EntryMessage && strings.Contains(string(entry.Data), "repair-") {
			t.Fatalf("a repair was written: %s", entry.Data)
		}
	}
}

func TestViewRebuildsACutOffReplyFromItsParts(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	writer, err := session.Create(ctx, store, session.New(), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	_ = writer.Record().Append(gotato.UserMessage("question"))
	for _, text := range []string{"half an ", "answer"} {
		if err := writer.Step(ctx, gotato.Step{Kind: gotato.StepReplyPart, MessageID: "reply-1", Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	// The instance stops mid-stream; another one takes the Session over.
	next, err := session.OpenWriter(ctx, store, writer.ID(), "instance-b")
	if err != nil {
		t.Fatal(err)
	}
	_ = next.Record().Append(gotato.UserMessage("again"))
	if err := next.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	view, err := session.View(ctx, store, writer.ID())
	if err != nil {
		t.Fatal(err)
	}
	messages := view.Messages()
	if len(messages) != 3 || messages[1].StopReason != gotato.StopCutOff || gotato.TextOf(messages[1]) != "half an answer" || gotato.TextOf(messages[2]) != "again" {
		t.Fatalf("view = %+v, want the cut-off reply between the two prompts", messages)
	}
	facts, err := session.Open(ctx, store, writer.ID())
	if err != nil {
		t.Fatal(err)
	}
	if facts.Len() != 2 {
		t.Fatalf("facts = %+v, want the two prompts only: reply parts are log", facts.Messages())
	}
}
