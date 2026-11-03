# Test with fakes

> [Wiki](../README.md) › [How-to](../README.md#how-to) › Test with fakes

By the end of this page you can test an agent, its tools and its HTTP endpoint with no provider and no credential, and every run gives the same result. The fakes live in the `testkit` package, which is part of the root module. You need the [first page](../tutorials/getting-started.md).

- [Which fake do I use?](#which-fake-do-i-use)
- [A model that calls a tool, then answers](#a-model-that-calls-a-tool-then-answers)
- [Read the Events of a Run](#read-the-events-of-a-run)
- [A slow model, and a Run that is canceled](#a-slow-model-and-a-run-that-is-canceled)
- [A model that must not be called too often](#a-model-that-must-not-be-called-too-often)
- [A model that decides from the request](#a-model-that-decides-from-the-request)
- [A model that fails](#a-model-that-fails)

## Which fake do I use?

Pick by what the test needs to show. A `Script` is the sequence of events one model call streams, built by `testkit.Text` and `testkit.ToolCalls`.

| You want | Use |
|---|---|
| a model that answers a fixed sequence of replies | `testkit.NewFakeModel(testkit.Text("a"), testkit.Text("b"))` |
| a model that calls a tool, then answers | `testkit.NewFakeModel(testkit.ToolCalls(call), testkit.Text("done"))` |
| the same, and a failure when it is called once too often | `testkit.NewReplayModel(...)` |
| a model that decides from the request, shared by several agents | `testkit.NewFuncModel(fn)`, with `testkit.ToolResults` and `testkit.UserText` |
| a model that streams slowly, to cut it half-way | a `FakeModel` with `Delay` set. A `FuncModel` has no `Delay`: wait inside `fn`, on `select { case <-time.After(d): case <-ctx.Done(): return nil, ctx.Err() }` |
| a model that waits until the test lets it go | a `FakeModel` with `Block` set (a channel the test closes) |
| a model that fails | a `FakeModel` with `Err` set |
| a model that echoes the prompt | `testkit.EchoModel{}` (answers `echo: <text>`) |
| a model that calls `demo.echo` on the prompt `use-tool` | `testkit.DemoModel{}`, with `testkit.DemoEchoTool()` |
| a tool that returns a fixed result, and records its calls | `testkit.NewFakeTool(id, text)`; read `Calls()` and `Uses()` |
| every Event of a Session, in order | `testkit.RecordEvents(s)`, or `testkit.RecordSubscription(sub)` |
| a Session Record that already holds a conversation | `testkit.NewRecord("question", "answer", ...)` |
| a Store of your own that keeps the contract | `storetest.RunRecords`, `RunLiveness` and `RunLister` in `store/storetest` ([Write a Store](write-a-store-adapter.md)) |

- A `FakeModel` repeats its last Script once the list ends, so a loop always finishes. A `ReplayModel` fails instead.
- A fake never touches the network. A test that needs a provider's wire quirks (a refusal as too long, a cut stream) belongs to the Gotato repository, which has a fake model endpoint for it under `internal/`; your own tests do not need it.

## A model that calls a tool, then answers

Each snippet below is a test function. They share one `_test.go` file, with these imports:

```go
import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	httpapi "github.com/jinhuang712/gotato/adapter/http"
	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/host"
	"github.com/jinhuang712/gotato/profile"
	"github.com/jinhuang712/gotato/store"
	"github.com/jinhuang712/gotato/testkit"
)
```

`NewFakeModel` takes one Script for each model call. The first asks for a tool, the second answers. `FakeTool` returns a fixed result and records what it was called with.

```go
func TestAgentCallsAToolThenAnswers(t *testing.T) {
	ctx := context.Background()
	lookup := testkit.NewFakeTool("lookup", "sunny")
	m := testkit.NewFakeModel(
		testkit.ToolCalls(gotato.ToolCall{ID: "c1", ToolID: "lookup", Arguments: []byte(`{"city":"Oslo"}`)}),
		testkit.Text("It is sunny in Oslo."),
	)

	s, err := gotato.NewSession(ctx, gotato.WithModel(m), gotato.WithTools(lookup))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	events := testkit.RecordEvents(s) // right after NewSession, before the first Prompt

	result, err := s.Prompt(ctx, gotato.UserMessage("weather in Oslo?"))
	if err != nil {
		t.Fatal(err)
	}

	if got := gotato.TextOf(*result.FinalMessage); got != "It is sunny in Oslo." {
		t.Fatalf("final = %q", got)
	}
	if lookup.Calls() != 1 || string(lookup.Uses()[0].Arguments) != `{"city":"Oslo"}` {
		t.Fatalf("tool uses = %v", lookup.Uses())
	}
	if m.Calls() != 2 {
		t.Fatalf("model calls = %d, want 2 (the call, then the answer)", m.Calls())
	}
	t.Log(events.Kinds())
	for _, kind := range []gotato.EventKind{gotato.EventKindToolStarted, gotato.EventKindToolEnded, gotato.EventKindRunEnded} {
		if !slices.Contains(events.Kinds(), kind) {
			t.Errorf("no %s event", kind)
		}
	}
	// The second model call saw the tool result.
	last, _ := m.LastRequest()
	if last.Messages[len(last.Messages)-1].Role != gotato.RoleToolResult {
		t.Errorf("the second request ends with %s", last.Messages[len(last.Messages)-1].Role)
	}
}
```

- The tool call names the tool by its `ToolID`, which is its ID in `NewFuncTool` or in `NewFakeTool`.
- `m.Calls()` is 2: the call that asked for the tool, and the one that answered after the Tool Result.
- `m.LastRequest()` is what the model was sent, so a test can check the Context: here, that the Tool Result came last.
- Use the Session in `defer s.Close(ctx)` as the first page does.

## Read the Events of a Run

`RecordEvents` subscribes to a Session and keeps every Event in order. Call it right after `NewSession`, before the first `Prompt`. In the test above, `events.Kinds()` was:

```text
[run_started message_committed turn_started context_built reply_started reply_ended
 tool_started tool_ended tool_result_committed turn_ended
 turn_started context_built reply_started text_delta reply_ended turn_ended run_ended]
```

- `events.Events()` returns the Events themselves, to switch on their type; `events.OfKind(kind)` returns those of one kind.
- A Session never waits for a subscriber, so a recorder does not change the Run. `Events()` waits until the recorder has read what the Session published.
- A Runner has no Session for the test to subscribe to. Collect the Events of a Run with the sink of `Runner.StreamRun`: `sink := func(e gotato.Event) error { kinds = append(kinds, e.Kind()); return nil }`, passed as its third argument.

## A slow model, and a Run that is canceled

A `FakeModel` keeps one script for the whole process. Every Run that calls it takes the next reply, so two Runs, or two Sessions, that share one `FakeModel` also share its script. When requests arrive in no fixed order, as in a server test, use a `FuncModel` that decides from the request instead.

Set `Delay` on a `FakeModel` and every event of its stream waits that long, so the stream is open while the test acts. Here the context ends first, and the Run settles as `deadline_exceeded`.

```go
func TestACanceledRunSettlesCanceled(t *testing.T) {
	m := testkit.NewFakeModel(testkit.Text("a long answer"))
	m.Delay = 200 * time.Millisecond // each event waits, so the stream is open for a while

	s, err := gotato.NewSession(context.Background(), gotato.WithModel(m))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	result, _ := s.Prompt(ctx, gotato.UserMessage("hi"))
	t.Log(result.Status)
	if result.Status != gotato.RunStatusDeadlineExceeded {
		t.Fatalf("status = %s", result.Status)
	}
}
```

- With `context.WithCancel` in place of the timeout, the Run settles as `canceled`.
- To hold the model until the test says go (a second message that arrives mid-Run), set `Block` to a channel and close it when the test is ready.

## A model that must not be called too often

A `ReplayModel` plays back recorded Scripts and fails when they run out, so a loop that makes one call too many is a failed test, not a silent repeat.

```go
func TestReplayModelFailsOnAnExtraCall(t *testing.T) {
	ctx := context.Background()
	m := testkit.NewReplayModel(testkit.Text("one"))
	s, _ := gotato.NewSession(ctx, gotato.WithModel(m))
	defer s.Close(ctx)

	if _, err := s.Prompt(ctx, gotato.UserMessage("first")); err != nil {
		t.Fatal(err)
	}
	if m.Remaining() != 0 {
		t.Fatalf("remaining = %d", m.Remaining())
	}
	result, err := s.Prompt(ctx, gotato.UserMessage("second"))
	t.Log(result.Status, err)
	if !errors.Is(err, io.ErrUnexpectedEOF) || result.Status != gotato.RunStatusFailed {
		t.Fatalf("status = %s, err = %v", result.Status, err)
	}
}
```

- `Remaining()` says how many recorded calls are left, so a test can also assert that all of them were used.
- The failed Run has the status `failed` and the code `model_failure`; the error wraps `io.ErrUnexpectedEOF`.

## A model that decides from the request

When several agents share a model, or the order of calls is not fixed, a fixed list of Scripts is racy. `NewFuncModel` answers each call from the request it gets.

```go
func TestFuncModelAnswersFromTheRequest(t *testing.T) {
	ctx := context.Background()
	m := testkit.NewFuncModel(func(_ context.Context, req gotato.ModelRequest) (testkit.Script, error) {
		return testkit.Text("you said: " + strings.TrimSpace(testkit.UserText(req))), nil
	})
	s, _ := gotato.NewSession(ctx, gotato.WithModel(m))
	defer s.Close(ctx)
	result, err := s.Prompt(ctx, gotato.UserMessage("ping"))
	if err != nil || gotato.TextOf(*result.FinalMessage) != "you said: ping" {
		t.Fatal(result, err)
	}
}
```

- `testkit.UserText(req)` is the text of every user message in the request, one per line, so trim it.
- `testkit.ToolResults(req)` is the text of every Tool Result, in order: a model that calls a tool until it has seen the result needs it.

## A model that fails

Set `Err` on a `FakeModel` and its `Stream` returns that error, as a provider outage would.

```go
func TestAModelThatFails(t *testing.T) {
	ctx := context.Background()
	m := testkit.NewFakeModel()
	m.Err = errors.New("provider down")
	s, _ := gotato.NewSession(ctx, gotato.WithModel(m))
	defer s.Close(ctx)
	result, err := s.Prompt(ctx, gotato.UserMessage("hi"))
	t.Log(result.Status, err)
}
```

The Run settles as `failed` with the code `model_failure`, and the error is returned with it. The result is a value, so a test reads the status before the error.


## Next

- Test a Runner and its HTTP handler: [Test a Runner and its endpoint](test-a-runner-and-its-endpoint.md).
- Test the tool and the model you wrote: [Test a tool and a model](test-a-tool-and-a-model.md). Write them: [Write a tool](write-a-tool.md) and [Wrap a model](wrap-a-model.md).
- What each Event means: [GLOSSARY.md](../../GLOSSARY.md).
