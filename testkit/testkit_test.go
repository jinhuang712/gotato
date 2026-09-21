package testkit_test

import (
	"context"
	"errors"
	"io"
	"testing"

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
