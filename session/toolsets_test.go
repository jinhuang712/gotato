package session_test

import (
	"context"
	"reflect"
	"testing"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/session"
)

func activated(name string) gotato.Event {
	return gotato.ToolSetActivated{Header: header(), ToolSet: name}
}

func TestActivatedToolSetsAreFactsOfTheRecord(t *testing.T) {
	if session.LayerOf(gotato.KindToolSetActivated) != session.LayerFact {
		t.Fatal("toolset_activated is not a fact")
	}
	ctx := context.Background()
	store := session.NewMemoryStore()
	w, err := session.Create(ctx, store, session.New(), "i1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(ctx, []gotato.Event{activated("files"), activated("git"), activated("files")}); err != nil {
		t.Fatal(err)
	}
	if got := w.Record().ActiveToolSets(); !reflect.DeepEqual(got, []string{"files", "git"}) {
		t.Fatalf("live ActiveToolSets = %v", got)
	}
	if err := w.Release(ctx); err != nil {
		t.Fatal(err)
	}
	opened, err := session.Open(ctx, store, w.ID())
	if err != nil {
		t.Fatal(err)
	}
	if got := opened.ActiveToolSets(); !reflect.DeepEqual(got, []string{"files", "git"}) {
		t.Fatalf("loaded ActiveToolSets = %v", got)
	}
}

func TestCheckpointKeepsActiveToolSets(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	w, err := session.Create(ctx, store, session.New(), "i1")
	if err != nil {
		t.Fatal(err)
	}
	commit(t, w, gotato.UserMessage("one"))
	if _, err := w.Write(ctx, []gotato.Event{activated("files")}); err != nil {
		t.Fatal(err)
	}
	w.Record().Checkpoint(gotato.CompactionOnDemand, []gotato.Message{gotato.AssistantMessage("summary")}, nil)
	if _, err := w.Write(ctx, []gotato.Event{activated("git")}); err != nil {
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
	if got := opened.ActiveToolSets(); !reflect.DeepEqual(got, []string{"files", "git"}) {
		t.Fatalf("ActiveToolSets after a Checkpoint = %v", got)
	}
	viewed, err := session.View(ctx, store, w.ID())
	if err != nil {
		t.Fatal(err)
	}
	if got := viewed.ActiveToolSets(); !reflect.DeepEqual(got, []string{"files", "git"}) {
		t.Fatalf("viewed ActiveToolSets = %v", got)
	}
}

func TestForkCopiesActiveToolSets(t *testing.T) {
	parent := session.New()
	if _, err := parent.Write(context.Background(), []gotato.Event{activated("files")}); err != nil {
		t.Fatal(err)
	}
	child := session.Fork(parent)
	if got := child.ActiveToolSets(); !reflect.DeepEqual(got, []string{"files"}) {
		t.Fatalf("fork ActiveToolSets = %v", got)
	}
	// The fork's first Checkpoint carries them, so a stored fork keeps them.
	session.StartLog(child)
	rebuilt, err := session.FromEntries(session.TakePending(child))
	if err != nil {
		t.Fatal(err)
	}
	if got := rebuilt.ActiveToolSets(); !reflect.DeepEqual(got, []string{"files"}) {
		t.Fatalf("rebuilt fork ActiveToolSets = %v", got)
	}
	// And the two are independent.
	if _, err := child.Write(context.Background(), []gotato.Event{activated("git")}); err != nil {
		t.Fatal(err)
	}
	if got := parent.ActiveToolSets(); !reflect.DeepEqual(got, []string{"files"}) {
		t.Fatalf("parent changed by its fork: %v", got)
	}
}

func TestDocumentRoundTripKeepsActiveToolSets(t *testing.T) {
	r := session.New()
	if _, err := r.Write(context.Background(), []gotato.Event{activated("files")}); err != nil {
		t.Fatal(err)
	}
	loaded, err := session.Load(r.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.ActiveToolSets(); !reflect.DeepEqual(got, []string{"files"}) {
		t.Fatalf("loaded ActiveToolSets = %v", got)
	}
}

func TestARecordWithoutActivationsRestoresNone(t *testing.T) {
	// A Record written before activation was a fact holds a session_created
	// with no tool_sets, and logged toolset_activated Events in a layer the
	// load does not read.
	r := session.New()
	session.StartLog(r)
	entries := session.TakePending(r)
	rebuilt, err := session.FromEntries(entries)
	if err != nil {
		t.Fatal(err)
	}
	if got := rebuilt.ActiveToolSets(); len(got) != 0 {
		t.Fatalf("ActiveToolSets = %v, want none", got)
	}
	if got := session.New().ActiveToolSets(); len(got) != 0 {
		t.Fatalf("a new Record has ActiveToolSets %v", got)
	}
}

var (
	_ gotato.ActiveToolSetSource = (*session.Record)(nil)
	_ gotato.ActiveToolSetSource = (*session.Writer)(nil)
)
