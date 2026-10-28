package testkit_test

import (
	"context"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/testkit"
)

func TestFakeToolAndEventRecorder(t *testing.T) {
	tool := testkit.NewFakeTool("fake", "result")
	model := testkit.NewFakeModel(
		testkit.ToolCalls(gotato.ToolCall{ID: "c1", ToolID: "fake", Arguments: []byte(`{"x":1}`)}),
		testkit.Text("done"),
	)
	agent, err := gotato.NewSession(context.Background(), gotato.WithModel(model), gotato.WithTools(tool))
	if err != nil {
		t.Fatal(err)
	}
	recorder := testkit.RecordEvents(agent)
	defer recorder.Close()
	defer func() { _ = agent.Close(context.Background()) }()
	if _, err := agent.Prompt(context.Background(), gotato.UserMessage("go")); err != nil {
		t.Fatal(err)
	}
	if tool.Calls() != 1 || string(tool.Uses()[0].Arguments) != `{"x":1}` {
		t.Fatalf("uses = %+v", tool.Uses())
	}
	kinds := recorder.Kinds()
	if len(kinds) == 0 || kinds[0] != gotato.EventKindRunStarted || kinds[len(kinds)-1] != gotato.EventKindRunEnded {
		t.Fatalf("kinds = %v", kinds)
	}
	if len(recorder.OfKind(gotato.EventKindToolEnded)) != 1 {
		t.Fatalf("tool end events = %d", len(recorder.OfKind(gotato.EventKindToolEnded)))
	}
	// Stored Events are numbered by their position in the Record, in order.
	var last uint64
	for _, event := range recorder.Events() {
		if event.Kind().Class() != gotato.EventClassStored {
			continue
		}
		if number := event.Head().Number; number <= last {
			t.Fatalf("%s numbered %d after %d", event.Kind(), number, last)
		} else {
			last = number
		}
	}
	recorder.Reset()
	if len(recorder.Events()) != 0 {
		t.Fatal("reset did not clear")
	}
}

func TestEventRecorderWaitsForAMatchingEvent(t *testing.T) {
	model := testkit.NewFakeModel(testkit.Text("done"))
	agent, err := gotato.NewSession(context.Background(), gotato.WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	recorder := testkit.RecordEvents(agent)
	defer recorder.Close()
	defer func() { _ = agent.Close(context.Background()) }()
	if recorder.Wait(10*time.Millisecond, func(e gotato.Event) bool { return e.Kind() == gotato.EventKindRunEnded }) {
		t.Fatal("Wait matched an Event before any Run")
	}
	if _, err := agent.Prompt(context.Background(), gotato.UserMessage("go")); err != nil {
		t.Fatal(err)
	}
	if !recorder.Wait(time.Second, func(e gotato.Event) bool { return e.Kind() == gotato.EventKindRunEnded }) {
		t.Fatalf("Wait did not see run_ended in %v", recorder.Kinds())
	}
}
