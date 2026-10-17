package session_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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

func TestReopeningByTheSameInstanceWritesNoClaim(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	created, err := session.Create(ctx, store, session.New(), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := session.OpenWriter(ctx, store, created.Record().ID(), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	if stats := reopened.Stats(); stats.Writes != 0 {
		t.Fatalf("reopening wrote %d times", stats.Writes)
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
