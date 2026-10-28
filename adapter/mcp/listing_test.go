package mcp_test

import (
	"context"
	"slices"
	"testing"

	gmcp "github.com/jinhuang712/gotato/adapter/mcp"
	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/testkit"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// A server that lists a tool twice, or one with a blank name, loses only that
// entry: the core refuses a ToolSet with a duplicate Tool ID whole, so the
// adapter leaves the bad entries out and reports them with Skipped.
func TestABadEntryInTheToolListLosesOnlyThatEntry(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "s", Version: "1"}, nil)
	schema := map[string]any{"type": "object", "properties": map[string]any{}}
	for _, name := range []string{"a", "b"} {
		server.AddTool(&sdk.Tool{Name: name, InputSchema: schema}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return textResult(name), nil
		})
	}
	server.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
			result, err := next(ctx, method, req)
			if list, ok := result.(*sdk.ListToolsResult); ok && err == nil {
				list.Tools = append(list.Tools, list.Tools[0], &sdk.Tool{Name: " ", InputSchema: schema})
			}
			return result, err
		}
	})
	set, err := gmcp.NewToolSet("srv", inMemory(t, server))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = set.Close() }()
	model := testkit.NewFakeModel(testkit.Text("ok"))
	s, err := gotato.NewSession(context.Background(), gotato.WithModel(model), gotato.WithFoldedToolSet(set))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close(context.Background()) }()
	if _, err := s.Prompt(context.Background(), gotato.UserMessage("hi")); err != nil {
		t.Fatal(err)
	}
	// With a good tool in the catalog the model is offered search_tools; a
	// ToolSet the core refused leaves it nothing.
	request, _ := model.LastRequest()
	if len(request.Tools) == 0 {
		t.Fatal("the model was offered no Tool: the server's good tools are lost with its bad entries")
	}
	tools, err := set.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools {
		names = append(names, tool.Spec().Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"a", "b"}) {
		t.Fatalf("tools = %v, want a and b", names)
	}
	skipped := set.Skipped()
	if len(skipped) != 2 || skipped[0].Name != "a" || skipped[1].Name != "" || skipped[0].Reason == "" || skipped[1].Reason == "" {
		t.Fatalf("Skipped = %+v, want the duplicate a and the blank name, each with a reason", skipped)
	}
}
