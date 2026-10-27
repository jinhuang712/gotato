package session_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

func loaded(id string) gotato.Event {
	return gotato.ToolLoaded{Header: header(), Tool: id, Why: gotato.ToolLoadedNames}
}

func unloaded(id string) gotato.Event {
	return gotato.ToolUnloaded{Header: header(), Tool: id, Why: gotato.ToolUnloadedCompaction}
}

func TestLoadedToolsAreFactsOfTheRecord(t *testing.T) {
	if session.LayerOf(gotato.KindToolLoaded) != session.LayerFact {
		t.Fatal("tool_loaded is not a fact")
	}
	ctx := context.Background()
	store := session.NewMemoryStore()
	w, err := session.Create(ctx, store, session.New(), "i1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(ctx, []gotato.Event{loaded("files.read"), loaded("mail.send"), loaded("files.read")}); err != nil {
		t.Fatal(err)
	}
	if got := w.Record().LoadedTools(); !reflect.DeepEqual(got, []string{"files.read", "mail.send"}) {
		t.Fatalf("live LoadedTools = %v", got)
	}
	if err := w.Release(ctx); err != nil {
		t.Fatal(err)
	}
	opened, err := session.Open(ctx, store, w.ID())
	if err != nil {
		t.Fatal(err)
	}
	if got := opened.LoadedTools(); !reflect.DeepEqual(got, []string{"files.read", "mail.send"}) {
		t.Fatalf("loaded LoadedTools = %v", got)
	}
}

func TestAnUnloadIsAFactAndRemovesTheTool(t *testing.T) {
	if session.LayerOf(gotato.KindToolUnloaded) != session.LayerFact {
		t.Fatal("tool_unloaded is not a fact")
	}
	ctx := context.Background()
	store := session.NewMemoryStore()
	w, err := session.Create(ctx, store, session.New(), "i1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(ctx, []gotato.Event{loaded("files.read"), loaded("mail.send"), unloaded("files.read")}); err != nil {
		t.Fatal(err)
	}
	if err := w.Release(ctx); err != nil {
		t.Fatal(err)
	}
	opened, err := session.Open(ctx, store, w.ID())
	if err != nil {
		t.Fatal(err)
	}
	if got := opened.LoadedTools(); !reflect.DeepEqual(got, []string{"mail.send"}) {
		t.Fatalf("LoadedTools = %v, want mail.send", got)
	}
}

func TestCheckpointKeepsLoadedTools(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	w, err := session.Create(ctx, store, session.New(), "i1")
	if err != nil {
		t.Fatal(err)
	}
	commit(t, w, gotato.UserMessage("one"))
	if _, err := w.Write(ctx, []gotato.Event{loaded("files.read")}); err != nil {
		t.Fatal(err)
	}
	w.Record().Checkpoint(gotato.CompactionOnDemand, []gotato.Message{gotato.AssistantMessage("summary")}, nil)
	if _, err := w.Write(ctx, []gotato.Event{loaded("mail.send")}); err != nil {
		t.Fatal(err)
	}
	if err := w.Release(ctx); err != nil {
		t.Fatal(err)
	}
	// The load starts at the Checkpoint, so "files" comes from it, and "git"
	// from the fact after it.
	opened, err := session.Open(ctx, store, w.ID())
	if err != nil {
		t.Fatal(err)
	}
	if got := opened.LoadedTools(); !reflect.DeepEqual(got, []string{"files.read", "mail.send"}) {
		t.Fatalf("LoadedTools after a Checkpoint = %v", got)
	}
	viewed, err := session.View(ctx, store, w.ID())
	if err != nil {
		t.Fatal(err)
	}
	if got := viewed.LoadedTools(); !reflect.DeepEqual(got, []string{"files.read", "mail.send"}) {
		t.Fatalf("viewed LoadedTools = %v", got)
	}
}

func TestForkCopiesLoadedTools(t *testing.T) {
	parent := session.New()
	if _, err := parent.Write(context.Background(), []gotato.Event{loaded("files.read")}); err != nil {
		t.Fatal(err)
	}
	child := session.Fork(parent)
	if got := child.LoadedTools(); !reflect.DeepEqual(got, []string{"files.read"}) {
		t.Fatalf("fork LoadedTools = %v", got)
	}
	// The fork's first Checkpoint carries them, so a stored fork keeps them.
	session.StartLog(child)
	rebuilt, err := session.FromEntries(session.TakePending(child))
	if err != nil {
		t.Fatal(err)
	}
	if got := rebuilt.LoadedTools(); !reflect.DeepEqual(got, []string{"files.read"}) {
		t.Fatalf("rebuilt fork LoadedTools = %v", got)
	}
	// And the two are independent.
	if _, err := child.Write(context.Background(), []gotato.Event{loaded("mail.send")}); err != nil {
		t.Fatal(err)
	}
	if got := parent.LoadedTools(); !reflect.DeepEqual(got, []string{"files.read"}) {
		t.Fatalf("parent changed by its fork: %v", got)
	}
}

func TestDocumentRoundTripKeepsLoadedTools(t *testing.T) {
	r := session.New()
	if _, err := r.Write(context.Background(), []gotato.Event{loaded("files.read")}); err != nil {
		t.Fatal(err)
	}
	loaded, err := session.Load(r.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.LoadedTools(); !reflect.DeepEqual(got, []string{"files.read"}) {
		t.Fatalf("loaded LoadedTools = %v", got)
	}
}

func TestARecordWithoutLoadsRestoresNone(t *testing.T) {
	// A Record written before loading was a fact holds a session_created
	// with no tool_sets, and logged tool_loaded Events in a layer the
	// load does not read.
	r := session.New()
	session.StartLog(r)
	entries := session.TakePending(r)
	rebuilt, err := session.FromEntries(entries)
	if err != nil {
		t.Fatal(err)
	}
	if got := rebuilt.LoadedTools(); len(got) != 0 {
		t.Fatalf("LoadedTools = %v, want none", got)
	}
	if got := session.New().LoadedTools(); len(got) != 0 {
		t.Fatalf("a new Record has LoadedTools %v", got)
	}
}

var (
	_ gotato.LoadedToolSource = (*session.Record)(nil)
	_ gotato.LoadedToolSource = (*session.Writer)(nil)
)
