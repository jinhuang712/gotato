package service_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// A fork inherits the Segments its parent closed. read_result in the fork
// finds a spilled call inside such a Segment in the log of the Session that
// wrote it, not in the fork's own log.
func TestForkReadsASpilledResultInAnInheritedSegment(t *testing.T) {
	ctx := context.Background()
	body := strings.Repeat("order-88501,paid,41.20,some more text on the line\n", 2000)
	model := testkit.NewFakeModel(
		testkit.ToolCalls(gotato.ToolCall{ID: "big-1", ToolID: "orders.export", Arguments: []byte(`{}`)}), testkit.Text("exported"), // parent Run 1
		testkit.Text("second"), // parent Run 2
		testkit.ToolCalls(gotato.ToolCall{ID: "read-1", ToolID: "read_result", Arguments: []byte(`{"ref":"result:big-1"}`)}), testkit.Text("read"), // fork Run
	)
	runner, err := service.New(service.Config{
		Store:  session.NewMemoryStore(),
		Agents: []service.Agent{{Name: "a", Model: model, Tools: []gotato.Tool{testkit.NewFakeTool("orders.export", body)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runner.Close(ctx) }()
	first, err := runner.Run(ctx, service.RunRequest{Prompt: "export"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "again"}); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Compact(ctx, first.SessionID, modelctx.CompactConfig{KeepMessages: 2}); err != nil {
		t.Fatal(err)
	}
	child, err := runner.Fork(ctx, first.SessionID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, byCall := child.SpilledResults(); len(byCall) != 0 || len(child.Segments()) == 0 {
		t.Fatalf("the fork should hold the call only inside a Segment: refs %v, %d Segments", byCall, len(child.Segments()))
	}
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: child.ID(), Prompt: "read it"}); err != nil {
		t.Fatal(err)
	}
	last := model.Requests()[len(model.Requests())-1]
	results := testkit.ToolResults(last)
	if got := results[len(results)-1]; !strings.HasPrefix(got, "Result result:big-1, ") || !strings.Contains(got, "order-88501") {
		t.Fatalf("read_result in the fork = %.200q", got)
	}
}
