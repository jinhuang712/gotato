package testkit_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/testkit"
)

func TestFakeModelRepeatsLastScriptAndRecordsRequests(t *testing.T) {
	model := testkit.NewFakeModel(testkit.Text("a"), testkit.Text("b"))
	agent, err := gotato.NewAgent(gotato.WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	for i, want := range []string{"a", "b", "b"} {
		result, err := agent.Prompt(context.Background(), gotato.UserMessage("q"))
		if err != nil || gotato.TextOf(*result.FinalMessage) != want {
			t.Fatalf("call %d = %v %v", i, result.FinalMessage, err)
		}
	}
	if model.Calls() != 3 || len(model.Requests()) != 3 {
		t.Fatalf("calls = %d", model.Calls())
	}
}

func TestReplayModelFailsWhenExhausted(t *testing.T) {
	model := testkit.NewReplayModel(testkit.Text("only"))
	agent, err := gotato.NewAgent(gotato.WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	if _, err := agent.Prompt(context.Background(), gotato.UserMessage("1")); err != nil {
		t.Fatal(err)
	}
	if model.Remaining() != 0 {
		t.Fatalf("remaining = %d", model.Remaining())
	}
	result, err := agent.Prompt(context.Background(), gotato.UserMessage("2"))
	if result.Status != gotato.RunFailed || !errors.Is(err, io.ErrUnexpectedEOF) && !gotato.IsCode(err, gotato.ErrModelFailure) {
		t.Fatalf("exhausted replay: %+v %v", result, err)
	}
}

func TestReplayModelExposesScriptExhausted(t *testing.T) {
	model := testkit.NewReplayModel()
	if _, err := model.Stream(context.Background(), gotato.ModelRequest{}); !errors.Is(err, testkit.ErrScriptExhausted) || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v", err)
	}
}

func TestFakeModelBlockHonorsCancellation(t *testing.T) {
	model := testkit.NewFakeModel(testkit.Text("x"))
	block := make(chan struct{})
	model.Block = block
	stream, err := model.Stream(context.Background(), gotato.ModelRequest{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := stream.Recv(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked Recv err = %v", err)
	}
	close(block)
	if _, err := stream.Recv(context.Background()); err != nil {
		t.Fatalf("Recv after unblock: %v", err)
	}
}

func TestFakeModelWithoutScriptsFails(t *testing.T) {
	model := testkit.NewFakeModel()
	if _, err := model.Stream(context.Background(), gotato.ModelRequest{}); err == nil {
		t.Fatal("FakeModel with no scripts returned a stream")
	}
}

func TestFakeModelRequestsAreIsolated(t *testing.T) {
	model := testkit.NewFakeModel(testkit.Text("a"))
	agent, err := gotato.NewAgent(gotato.WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	if _, err := agent.Prompt(context.Background(), gotato.UserMessage("q")); err != nil {
		t.Fatal(err)
	}
	requests := model.Requests()
	if len(requests) == 0 || len(requests[0].Messages) == 0 {
		t.Fatalf("requests = %+v", requests)
	}
	requests[0].Messages[0].Parts[0].Text = "mutated"
	if gotato.TextOf(model.Requests()[0].Messages[0]) == "mutated" {
		t.Fatal("mutating a returned request reached the recording")
	}
}

func TestLoadReplay(t *testing.T) {
	model, err := testkit.LoadReplay([]byte(`[[{"kind":"text_delta","text":"hi"},{"kind":"done","stop_reason":"end_turn"}]]`))
	if err != nil {
		t.Fatal(err)
	}
	if model.Remaining() != 1 {
		t.Fatalf("remaining = %d", model.Remaining())
	}
}

func TestFakeToolAndEventRecorder(t *testing.T) {
	tool := testkit.NewFakeTool("fake", "result")
	recorder := testkit.NewEventRecorder()
	model := testkit.NewFakeModel(
		testkit.ToolCalls(gotato.ToolCall{ID: "c1", ToolID: "fake", Arguments: []byte(`{"x":1}`)}),
		testkit.Text("done"),
	)
	agent, err := gotato.NewAgent(gotato.WithModel(model), gotato.WithTool(tool), gotato.WithExtension(recorder))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	if _, err := agent.Prompt(context.Background(), gotato.UserMessage("go")); err != nil {
		t.Fatal(err)
	}
	if tool.Calls() != 1 || string(tool.Uses()[0].ArgumentsJSON) != `{"x":1}` {
		t.Fatalf("uses = %+v", tool.Uses())
	}
	kinds := recorder.Kinds()
	if kinds[0] != gotato.EventAgentStart || kinds[len(kinds)-1] != gotato.EventAgentEnd {
		t.Fatalf("kinds = %v", kinds)
	}
	if len(recorder.OfKind(gotato.EventToolExecutionEnd)) != 1 {
		t.Fatalf("tool end events = %d", len(recorder.OfKind(gotato.EventToolExecutionEnd)))
	}
	recorder.Reset()
	if len(recorder.Events()) != 0 {
		t.Fatal("reset did not clear")
	}
}

func TestSessionFixture(t *testing.T) {
	s := testkit.NewSession("q", "a", "q2")
	messages := s.Messages()
	if len(messages) != 3 || messages[1].Role != gotato.RoleAssistant || messages[2].ID != "fixture-3" {
		t.Fatalf("fixture = %+v", messages)
	}
}

func TestFakeToolUsesAreIsolated(t *testing.T) {
	tool := testkit.NewFakeTool("fake", "result")
	use := gotato.ToolUse{
		ArgumentsJSON: []byte(`{"x":1}`),
		Result:        &gotato.ToolResult{Status: gotato.ToolResultOK, Content: []gotato.ContentPart{{Kind: gotato.ContentText, Text: "r"}}},
	}
	if _, err := tool.Execute(context.Background(), use, nil); err != nil {
		t.Fatal(err)
	}
	uses := tool.Uses()
	uses[0].ArgumentsJSON[0] = 'X'
	uses[0].Result.Content[0].Text = "mutated"

	again := tool.Uses()
	if string(again[0].ArgumentsJSON) != `{"x":1}` {
		t.Fatalf("arguments mutation reached the recording: %s", again[0].ArgumentsJSON)
	}
	if again[0].Result == nil || again[0].Result.Content[0].Text != "r" {
		t.Fatalf("result mutation reached the recording: %+v", again[0].Result)
	}
}

func TestEventRecorderEventsAreIsolated(t *testing.T) {
	recorder := testkit.NewEventRecorder()
	payload := map[string]any{"summary": map[string]any{"tool_results": []map[string]any{{"status": "ok"}}}}
	if err := recorder.Observe(context.Background(), gotato.Event{Kind: gotato.EventTurnEnd, Payload: payload}); err != nil {
		t.Fatal(err)
	}

	events := recorder.Events()
	events[0].Payload["summary"].(map[string]any)["tool_results"].([]map[string]any)[0]["status"] = "reader"

	again := recorder.Events()
	status := again[0].Payload["summary"].(map[string]any)["tool_results"].([]map[string]any)[0]["status"]
	if status != "ok" {
		t.Fatalf("payload mutation reached the recording: %v", status)
	}
}
