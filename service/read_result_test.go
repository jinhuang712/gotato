package service_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// A Tool Result of 1 MB is spilled by a Runner. read_result is in the tool list
// before the result is spilled and after it, byte for byte the same; the model
// reads the rest through it; and an instance that loads the Session builds the
// same request as the one that ran.
func TestARunnerSpillsALargeResultAndReadsItBack(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	body := strings.Repeat("order-88501,paid,41.20,some more text on the line\n", 21000)
	model := testkit.NewFakeModel(
		testkit.ToolCalls(gotato.ToolCall{ID: "big-1", ToolID: "orders.export", Arguments: []byte(`{}`)}), testkit.Text("exported"), // Run 1
		testkit.ToolCalls(gotato.ToolCall{ID: "read-1", ToolID: "read_result", Arguments: []byte(`{"ref":"result:big-1","page":2}`)}), testkit.Text("read"), // Run 2
	)
	runner, err := service.New(service.Config{
		Store:  store,
		Agents: []service.Agent{{Name: "a", Model: model, Tools: []gotato.Tool{testkit.NewFakeTool("orders.export", body)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := runner.Run(ctx, service.RunRequest{Prompt: "export"})
	if err != nil {
		t.Fatal(err)
	}
	id := first.SessionID
	requests := model.Requests()
	if tools := toolIDsOf(requests[0]); !slices.Contains(tools, "read_result") {
		t.Fatalf("the first request offers %v: read_result should be there before anything is spilled", tools)
	}
	if before, after := toolIDsOf(requests[0]), toolIDsOf(requests[1]); !slices.Equal(before, after) {
		t.Fatalf("the tool list changed on the Turn the result was spilled: %v then %v", before, after)
	}
	results := testkit.ToolResults(requests[1])
	if len(results) != 1 || len(results[0]) > 9000 || !strings.Contains(results[0], `read_result(ref="result:big-1", page=2)`) {
		t.Fatalf("the second request holds %d results, the first of %d bytes", len(results), len(results[0]))
	}

	// Another instance loads the Session and builds the request the first would.
	other, err := service.New(service.Config{Store: store, Agents: []service.Agent{{Name: "a", Model: model, Tools: []gotato.Tool{testkit.NewFakeTool("orders.export", body)}}}})
	if err != nil {
		t.Fatal(err)
	}
	a, tools := inspectJSON(t, runner, id)
	b, _ := inspectJSON(t, other, id)
	if a != b || !slices.Contains(tools, "read_result") || len(a) > 40<<10 {
		t.Fatalf("two instances build different requests, or the request is large (%d bytes)", len(a))
	}

	// The model reads page 2 through the other instance, from the store.
	if _, err := other.Run(ctx, service.RunRequest{SessionID: id, Prompt: "read more"}); err != nil {
		t.Fatal(err)
	}
	last := model.Requests()[len(model.Requests())-1]
	page := testkit.ToolResults(last)
	if got := page[len(page)-1]; !strings.HasPrefix(got, "Result result:big-1, ") || !strings.Contains(got, "Page 2 of ") || !strings.Contains(got, "order-88501") {
		t.Fatalf("page 2 = %.200q", got)
	}
}

// An Agent with no tool is offered no read_result: there is nothing to spill.
func TestAnAgentWithNoToolIsOfferedNoReadResult(t *testing.T) {
	runner, err := service.New(service.Config{Store: session.NewMemoryStore(), Agents: []service.Agent{{Name: "a", Model: testkit.NewFakeModel(testkit.Text("hi"))}}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), service.RunRequest{Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if _, tools := inspectJSON(t, runner, result.SessionID); slices.Contains(tools, "read_result") {
		t.Fatalf("tools = %v", tools)
	}
}
