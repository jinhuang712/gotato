package session_test

import (
	"context"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

// spillRecord stores a Record named id whose log holds a tool_result_spilled
// Entry with body at position padTo. holder, when not nil, is the Message the
// Record starts with (it carries the reference).
func spillRecord(t *testing.T, store session.Store, id, body string, holder *gotato.Message, padTo uint64) {
	t.Helper()
	ctx := context.Background()
	options := []session.RecordOption{session.WithID(id)}
	if holder != nil {
		options = append(options, session.WithMessages(*holder))
	}
	w, err := session.CreateWriter(ctx, store, session.New(options...), "i1", session.WithIdle())
	if err != nil {
		t.Fatal(err)
	}
	for w.Record().Head()+1 < padTo {
		if _, err := w.Write(ctx, []gotato.Event{gotato.MetadataChanged{Header: gotato.NewHeader(gotato.SessionID(id), "", 0), Key: "k", Value: "v"}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.Write(ctx, []gotato.Event{gotato.ToolResultSpilled{
		Header: gotato.NewHeader(gotato.SessionID(id), "", 0), CallID: "call-1", ToolID: "t", Bytes: len(body), Body: body}}); err != nil {
		t.Fatal(err)
	}
}

func spilledHolder() gotato.Message {
	return gotato.Message{ID: "m1", Role: gotato.RoleToolResult, ToolResult: &gotato.ToolResult{CallID: "call-1", Spilled: &gotato.SpilledResult{Seq: 5, Bytes: 10}}}
}

// A fork reads a spilled body from its parent's log. When the parent is deleted
// and another Record takes its ID, the fork gets "pruned", not that Record's
// Entry at the same position.
func TestForkSpilledBodyOfARecreatedParentIsPruned(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	message := spilledHolder()
	spillRecord(t, store, "parent", "ORIGINAL BODY", &message, 5)
	parent, err := session.Open(ctx, store, "parent")
	if err != nil {
		t.Fatal(err)
	}
	fork := session.Fork(parent, session.WithID("fork"))
	ref := *fork.Messages()[0].ToolResult.Spilled
	if ref.SessionID != "parent" || ref.Seq != 5 || ref.RecordEpoch == "" {
		t.Fatalf("fork reference = %+v, want Seq 5 of the parent with its epoch", ref)
	}
	if body, pruned, err := fork.SpilledBody(ctx, store, ref); err != nil || pruned || body != "ORIGINAL BODY" {
		t.Fatalf("before the delete: body %q pruned %v err %v", body, pruned, err)
	}
	if err := store.Delete(ctx, "parent"); err != nil {
		t.Fatal(err)
	}
	if _, pruned, err := fork.SpilledBody(ctx, store, ref); err != nil || !pruned {
		t.Fatalf("parent gone: pruned %v err %v, want pruned", pruned, err)
	}
	spillRecord(t, store, "parent", "SECRET BODY of an unrelated Session", nil, 5)
	body, pruned, err := fork.SpilledBody(ctx, store, ref)
	if err != nil {
		t.Fatal(err)
	}
	if !pruned {
		t.Fatalf("the fork read %q from a Record that is not its parent; want pruned", body)
	}
}

// A fork of a fork keeps the epoch of the Record that holds the body, not its
// parent's.
func TestForkOfForkSpilledBodyKeepsTheSourceEpoch(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	message := spilledHolder()
	spillRecord(t, store, "root", "ORIGINAL BODY", &message, 5)
	root, err := session.Open(ctx, store, "root")
	if err != nil {
		t.Fatal(err)
	}
	middle := session.Fork(root, session.WithID("middle"))
	if _, err := session.CreateWriter(ctx, store, middle, "i1", session.WithIdle()); err != nil {
		t.Fatal(err)
	}
	leaf := session.Fork(middle, session.WithID("leaf"))
	ref := *leaf.Messages()[0].ToolResult.Spilled
	if ref.SessionID != "root" || ref.RecordEpoch != root.RecordEpoch() {
		t.Fatalf("leaf reference = %+v, want the root with its epoch %q", ref, root.RecordEpoch())
	}
	if body, pruned, err := leaf.SpilledBody(ctx, store, ref); err != nil || pruned || body != "ORIGINAL BODY" {
		t.Fatalf("body %q pruned %v err %v", body, pruned, err)
	}
	if err := store.Delete(ctx, "root"); err != nil {
		t.Fatal(err)
	}
	spillRecord(t, store, "root", "SECRET", nil, 5)
	if body, pruned, err := leaf.SpilledBody(ctx, store, ref); err != nil || !pruned {
		t.Fatalf("recreated root: body %q pruned %v err %v, want pruned", body, pruned, err)
	}
}
