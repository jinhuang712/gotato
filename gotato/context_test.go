package gotato

import (
	"context"
	"errors"
	"strconv"
	"testing"
)

func TestContextBuilderShapesModelViewAndEmitsEvent(t *testing.T) {
	model := &testModel{scripts: [][]ModelEvent{{{Kind: ModelEventKindTextDelta, Text: "ok"}, {Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}}}}
	builder := ContextBuilderFunc(func(_ context.Context, snapshot ContextSnapshot) (ModelContext, error) {
		// Keep only the last Message.
		return ModelContext{
			SystemInstructions: "override",
			Messages:           snapshot.Messages[len(snapshot.Messages)-1:],
			// A builder key that names a count must not replace the count.
			Metadata: map[string]string{"strategy": "last_only", "source_messages": "99", "dropped_messages": strconv.Itoa(len(snapshot.Messages) - 1)},
		}, nil
	})
	transcript := &recordingTranscript{messages: []Message{UserMessage("old"), AssistantMessage("older answer")}}
	agent, err := NewSession(context.Background(), WithModel(model), WithInstruction("base"), WithTranscript(transcript), WithContextBuilder(builder))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	stream, err := agent.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Prompt(context.Background(), UserMessage("new")); err != nil {
		t.Fatal(err)
	}
	request := model.lastRequest()
	if request.SystemInstructions != "override" || len(request.Messages) != 1 || TextOf(request.Messages[0]) != "new" {
		t.Fatalf("model saw %+v", request)
	}
	// The Session still has everything: Context is a projection.
	if got := len(transcript.Messages()); got != 4 {
		t.Fatalf("transcript length = %d, want 4", got)
	}
	var built *ContextBuilt
	for {
		event, err := stream.Next(context.Background())
		if err != nil {
			break
		}
		if e, ok := event.(ContextBuilt); ok {
			built = &e
		}
		if _, ok := event.(RunEnded); ok {
			break
		}
	}
	if built == nil {
		t.Fatal("no context_built event")
	}
	if built.Strategy != "last_only" || built.Messages != 1 || built.SourceMessages != 3 || built.SelectedMessages != 1 {
		t.Fatalf("ContextBuilt = %+v", *built)
	}
	if built.Metadata["source_messages"] != "99" || built.Metadata["dropped_messages"] != "2" || built.Metadata["strategy"] != "last_only" {
		t.Fatalf("ContextBuilt metadata = %v", built.Metadata)
	}
}

func TestContextBuilderFailureSettlesRun(t *testing.T) {
	model := &testModel{scripts: [][]ModelEvent{{{Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}}}}
	builder := ContextBuilderFunc(func(context.Context, ContextSnapshot) (ModelContext, error) {
		return ModelContext{}, errors.New("boom")
	})
	agent, err := NewSession(context.Background(), WithModel(model), WithContextBuilder(builder))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	result, err := agent.Prompt(context.Background(), UserMessage("hi"))
	if !IsCode(err, CodeExtensionFailure) || result.Status != RunStatusFailed {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestRenderBlocks(t *testing.T) {
	got := renderBlocks([]ContextBlock{
		{Tag: "resource", Attrs: map[string]string{"path": "a.md", "lines": "1-2"}, Text: "x"},
		{Text: "bare"},
	})
	want := "<resource lines=\"1-2\" path=\"a.md\">x</resource>\nbare"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
