package service_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// A fork inherits the Messages of its parent, and with them the refs of the
// spilled results. The bodies stay in the parent's log, so the ref names it.
func TestForkKeepsSpilledResultsReadable(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	tool, err := gotato.NewFuncTool("large", "large result", func(context.Context, struct{}) (string, error) {
		return strings.Repeat("payload\n", 10000), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	model := testkit.NewFakeModel(testkit.ToolCalls(gotato.ToolCall{ID: "large-call", ToolID: "large", Arguments: []byte(`{}`)}), testkit.Text("done"))
	r, err := service.New(service.Config{Store: store, Agents: []service.Agent{{Name: "large", Model: model, Tools: []gotato.Tool{tool}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(ctx) }()
	out, err := r.Run(ctx, service.RunRequest{Prompt: "export"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := r.Fork(ctx, out.SessionID, "")
	if err != nil {
		t.Fatal(err)
	}
	grandchild, err := r.Fork(ctx, child.ID(), "")
	if err != nil {
		t.Fatal(err)
	}
	for name, record := range map[string]*session.Record{"child": child, "grandchild": grandchild} {
		calls, refs := record.SpilledResults()
		if len(calls) != 1 {
			t.Fatalf("%s: spilled refs=%v", name, calls)
		}
		if got := refs[calls[0]].SessionID; got != gotato.SessionID(out.SessionID) {
			t.Fatalf("%s: ref names %q, want the Session that wrote the body", name, got)
		}
		body, pruned, err := record.SpilledBody(ctx, store, refs[calls[0]])
		if err != nil || pruned || len(body) != 80000 {
			t.Fatalf("%s: body bytes=%d pruned=%v err=%v", name, len(body), pruned, err)
		}
	}
	// The parent's own Message is unchanged.
	parent, err := r.Record(ctx, out.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	pcalls, prefs := parent.SpilledResults()
	if prefs[pcalls[0]].SessionID != "" {
		t.Fatalf("parent ref names %q", prefs[pcalls[0]].SessionID)
	}
	// With the parent deleted the body is gone, which reads as pruned.
	if err := r.DeleteSession(ctx, out.SessionID); err != nil {
		t.Fatal(err)
	}
	calls, refs := child.SpilledResults()
	_, pruned, err := child.SpilledBody(ctx, store, refs[calls[0]])
	if err == nil && !pruned {
		t.Fatal("body still readable after the parent was deleted")
	}
}
