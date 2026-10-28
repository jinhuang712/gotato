package testkit_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/testkit"
)

func TestFakeModelRepeatsLastScriptAndRecordsRequests(t *testing.T) {
	model := testkit.NewFakeModel(testkit.Text("a"), testkit.Text("b"))
	agent, err := gotato.NewSession(context.Background(), gotato.WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
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
	agent, err := gotato.NewSession(context.Background(), gotato.WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	if _, err := agent.Prompt(context.Background(), gotato.UserMessage("1")); err != nil {
		t.Fatal(err)
	}
	if model.Remaining() != 0 {
		t.Fatalf("remaining = %d", model.Remaining())
	}
	result, err := agent.Prompt(context.Background(), gotato.UserMessage("2"))
	if result.Status != gotato.RunStatusFailed || !errors.Is(err, io.ErrUnexpectedEOF) && !gotato.IsCode(err, gotato.CodeModelFailure) {
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
	agent, err := gotato.NewSession(context.Background(), gotato.WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
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

func TestReplayModelCountsItsRecordedCalls(t *testing.T) {
	model := testkit.NewReplayModel(testkit.Text("hi"))
	if model.Remaining() != 1 {
		t.Fatalf("remaining = %d", model.Remaining())
	}
}

func TestFakeModelDelayWaitsBeforeEachEvent(t *testing.T) {
	model := testkit.NewFakeModel(testkit.Text("x"))
	model.Delay = 30 * time.Millisecond
	stream, err := model.Stream(context.Background(), gotato.ModelRequest{})
	if err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if _, err := stream.Recv(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Recv inside the delay: err = %v", err)
	}
	start := time.Now()
	if _, err := stream.Recv(context.Background()); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 20*time.Millisecond {
		t.Fatal("Recv returned before the delay")
	}
}
