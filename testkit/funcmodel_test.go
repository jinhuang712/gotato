package testkit_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	gotato "github.com/jinhuang712/gotato/gotato-core"
	"github.com/jinhuang712/gotato/testkit"
)

func TestFuncModelAnswersFromTheRequest(t *testing.T) {
	echo, err := gotato.NewFuncTool("echo", "echo", func(_ context.Context, in struct {
		Text string `json:"text"`
	}) (string, error) {
		return "echoed " + in.Text, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	model := testkit.NewFuncModel(func(_ context.Context, req gotato.ModelRequest) (testkit.Script, error) {
		if results := testkit.ToolResults(req); len(results) > 0 {
			return testkit.Text("saw " + results[len(results)-1]), nil
		}
		if strings.Contains(testkit.UserText(req), "please echo") {
			return testkit.ToolCalls(gotato.ToolCall{ID: "c1", ToolID: "echo", Arguments: []byte(`{"text":"hi"}`)}), nil
		}
		return testkit.Text("nothing to do"), nil
	})
	agent, err := gotato.NewSession(gotato.WithModel(model), gotato.WithTool(echo))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	result, err := agent.Prompt(context.Background(), gotato.UserMessage("please echo"))
	if err != nil {
		t.Fatal(err)
	}
	if got := gotato.TextOf(*result.FinalMessage); !strings.Contains(got, "echoed hi") {
		t.Fatalf("final = %q", got)
	}
	if model.Calls() != 2 || len(model.Requests()) != 2 {
		t.Fatalf("calls = %d", model.Calls())
	}
}

func TestFuncModelErrorsAndCancellation(t *testing.T) {
	boom := errors.New("provider down")
	failing := testkit.NewFuncModel(func(context.Context, gotato.ModelRequest) (testkit.Script, error) { return nil, boom })
	if _, err := failing.Stream(context.Background(), gotato.ModelRequest{}); !errors.Is(err, boom) {
		t.Fatalf("error = %v", err)
	}
	gate := make(chan struct{})
	blocking := testkit.NewFuncModel(func(ctx context.Context, _ gotato.ModelRequest) (testkit.Script, error) {
		select {
		case <-gate:
			return testkit.Text("released"), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := blocking.Stream(ctx, gotato.ModelRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled call = %v", err)
	}
}

func TestFuncModelGetsAnIsolatedRequest(t *testing.T) {
	model := testkit.NewFuncModel(func(_ context.Context, req gotato.ModelRequest) (testkit.Script, error) {
		req.Messages[0].Parts[0].Text = "mutated"
		return testkit.Text("ok"), nil
	})
	req := gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("original")}}
	if _, err := model.Stream(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := gotato.TextOf(model.Requests()[0].Messages[0]); got != "original" {
		t.Fatalf("recorded request mutated to %q", got)
	}
}
