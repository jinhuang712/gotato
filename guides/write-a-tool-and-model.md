# Write a Tool, a ToolSet and a Model

By the end of this page you have given an agent a tool of your own, grouped tools into a ToolSet, wrapped a Model, and tested all of it without a network. You need the [first page](getting-started.md). Every snippet here was compiled and run as a test against the root module.

**What is here**

| Group | Item | In one line |
|---|---|---|
| **Tools** | [A function tool](#a-function-tool) | `NewFuncTool` turns a Go function into a Tool; its input struct is the schema. |
| | [An Effect](#give-a-tool-an-effect) | `ToolWithEffect` says what a Tool changes, so a Session can cap it. |
| | [A Tool type](#a-tool-type) | `Spec` and `Execute` by hand, for a Tool that holds state or needs its own schema. |
| | [Errors the Model reads](#errors-the-model-reads) | `PublicError`, and a failed Tool Result. |
| | [A ToolSet](#a-toolset) | A named group of Tools, installed active, listed or folded. |
| **Models** | [A Model wrapper](#a-model-wrapper) | Wrap a `gotato.Model` to log, measure or rewrite. |
| | [Failures that say what to do](#failures-that-say-what-to-do) | `Retryable()` and `ContextOverflow()` on an error. |
| **Tests** | [Test with scripted fakes](#test-with-scripted-fakes) | `testkit.NewFakeModel` plays the model, your Tool runs for real. |

## Tools

A Tool is one capability a Session can call. Most are a function; a few are a type.

### A function tool

`NewFuncTool` takes a name, a description and a function. The function's input type must be a struct, and its fields become the JSON Schema the model sees.

```go
func WeatherTool() (gotato.Tool, error) {
	tool, err := gotato.NewFuncTool("weather", "Returns the weather of a city.", func(_ context.Context, in struct {
		City string `json:"city" description:"the city to look up"`
		Unit string `json:"unit,omitempty" description:"temperature unit" enum:"c,f"`
	}) (string, error) {
		return "sunny in " + in.City, nil
	})
	if err != nil {
		return nil, err
	}
	return gotato.ToolWithEffect(tool, gotato.ToolEffectRead), nil
}
```

- **Field rules follow `encoding/json`.** The `json` tag renames a field. `omitempty`, `omitzero` or a pointer makes it optional. `description` documents it and `enum` lists the allowed strings.
- **The schema it derives** for this function, as the Session sends it:

  ```json
  {"additionalProperties":false,"properties":{"city":{"description":"the city to look up","type":"string"},"unit":{"description":"temperature unit","enum":["c","f"],"type":"string"}},"required":["city"],"type":"object"}
  ```

- **The function may return a `string`, a `[]ContentPart`, a `gotato.ToolResult`, or any value that encodes to JSON.** A string is the common case.
- **Progress.** `NewFuncToolWithProgress` gives the function a `ToolProgressFunc`; call it with a line of text while the call runs, and subscribers see it as a `ToolProgress` Event.

Gotato checks the model's arguments against the schema before your function runs. Arguments that do not fit never reach you: they go back to the model as a failed Tool Result, and it corrects them. With `{"city":5}` the model reads `tool_argument_failure: weather: arguments.city must be string`.

### Give a Tool an Effect

An Effect says what a Tool changes. From weakest to strongest it is `ToolEffectRead`, `ToolEffectWriteLocal`, `ToolEffectWriteExternal`, `ToolEffectDestructive`. A Tool with no Effect ranks as destructive, so a filter that does not know a Tool fails closed.

- **For a function tool,** wrap it with `gotato.ToolWithEffect(tool, effect)`, as above.
- **For a Tool type,** set `ToolSpec.Effect`.
- **A Session caps what its Tools may do** with `gotato.WithMaxEffect`. A Tool above the cap is hidden from the model, and a call to it fails.

```go
s, err := gotato.NewSession(ctx,
	gotato.WithModel(model),
	gotato.WithTools(tool), // a CloseTicket, with an Effect of write_external
	gotato.WithMaxEffect(gotato.ToolEffectRead),
)
// s.Tools() is empty: the model is never offered close_ticket.
```

On the service side, `service.Agent` has a `MaxEffect` field with the same cap, so one Agent can be read-only and another not. A `PreToolUse` Extension can gate a single call by its Effect; see [the extension points](extension-points.md).

### A Tool type

Write the type yourself when the Tool holds state (a client, a database handle), or when you want the schema in your own hands. A Tool has two methods, `Spec` and `Execute`.

```go
// CloseTicket is a Tool written as a type, for a tool that holds state.
type CloseTicket struct {
	Tickets map[string]bool // id -> open
}

func (t *CloseTicket) Spec() gotato.ToolSpec {
	return gotato.ToolSpec{
		ID:          "close_ticket",
		Description: "Closes a ticket by ID.",
		InputSchema: []byte(`{
			"type": "object",
			"properties": {"id": {"type": "string", "description": "the ticket ID"}},
			"required": ["id"],
			"additionalProperties": false
		}`),
		Effect: gotato.ToolEffectWriteExternal,
	}
}

func (t *CloseTicket) Execute(ctx context.Context, use gotato.ToolUse, progress gotato.ToolProgressFunc) (gotato.ToolResult, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(use.Arguments, &in); err != nil {
		return gotato.ToolResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return gotato.ToolResult{}, err
	}
	open, ok := t.Tickets[in.ID]
	if !ok {
		return gotato.ToolResult{}, ErrNoSuchTicket{ID: in.ID} // the Model reads Public()
	}
	if !open {
		// A failure the Model should read, returned as a result and not as an error.
		return gotato.ToolResult{
			Status:    gotato.ToolResultStatusFailed,
			SafeError: "ticket " + in.ID + " is already closed",
		}, nil
	}
	progress("closing " + in.ID)
	t.Tickets[in.ID] = false
	return gotato.ToolResult{
		Status:  gotato.ToolResultStatusOK,
		Content: []gotato.ContentPart{{Kind: gotato.ContentKindText, Text: "closed " + in.ID}},
	}, nil
}
```

The rules a Tool keeps:

- **`ToolSpec.ID` is the Tool's unique name.** `InputSchema` is the JSON Schema the arguments are checked against before `Execute` runs, so `Execute` can decode without defending against a missing field.
- **Honor `ctx`.** The Session cancels it when the Run is aborted or the call passes its deadline. A Tool that ignores it holds the Run until it returns.
- **Thread safety.** A Turn may run its Tool calls in parallel. Set `ToolSpec.Sequential` for a Tool that must never run beside another Tool of the same Turn, and guard shared state in the Tool itself (the sketch above does not, because the example runs one call at a time).
- **A panic becomes a failed Tool Result,** and does not take the process down.
- **Install it** with `gotato.WithTools(tool)` on a Session, or in `service.Agent.Tools`.

### Errors the Model reads

The text of an error you return from `Execute` is not sent on: it may carry a host, a query or a secret. The model sees `tool failed` and a code. To say why in words the model can act on, return an error that implements `gotato.PublicError`:

```go
// ErrNoSuchTicket is an error whose text the Model may read.
type ErrNoSuchTicket struct{ ID string }

func (e ErrNoSuchTicket) Error() string  { return "ticket " + e.ID + " not found" }
func (e ErrNoSuchTicket) Public() string { return "there is no ticket " + e.ID }
```

- **`Error()` is for your logs,** and **`Public()` is for the model and every subscriber.** Put nothing in `Public()` that you would not show a user.
- **A failure the model should read, without an error type,** is a returned `ToolResult` with `Status: ToolResultStatusFailed` and a `SafeError`, as `CloseTicket` does for a ticket that is already closed.
- **Statuses.** `ToolResultStatusOK` and `ToolResultStatusFailed` are yours to set. `Blocked` (an Extension refused the call) and `Canceled` are set by the Session.

### A ToolSet

A ToolSet is a named group of Tools from one source, such as an MCP server or one internal system. It has a `Spec` (a name and a description) and a `Tools` method that returns its Tools.

```go
// TicketTools is a ToolSet: a named group of Tools from one source.
type TicketTools struct{ Close *CloseTicket }

func (TicketTools) Spec() gotato.ToolSetSpec {
	return gotato.ToolSetSpec{Name: "tickets", Description: "Ticket system tools."}
}

func (s TicketTools) Tools(context.Context) ([]gotato.Tool, error) {
	return []gotato.Tool{s.Close}, nil
}
```

- **A Tool's ID in the Session is `<set>.<tool>`,** here `tickets.close_ticket`. That is the name the model calls, and the name in the Record.
- **Three ways to install it,** by how much the model sees:

  | Option | The model sees | Use it for |
  |---|---|---|
  | `gotato.WithActiveToolSet(set)` | every Tool, always, as if they were base Tools | a few Tools every Run needs |
  | `gotato.WithToolSet(set)` (the default) | the set's description and its Tool names; it loads the ones it needs with `search_tools` | a set of a dozen Tools |
  | `gotato.WithFoldedToolSet(set)` | the description and a count; it lists the Tools, then loads them | a large set, such as a big MCP server |

- **`Tools` runs on `ctx`,** may fail, and must be deterministic. A failure marks the set unavailable and does not stop the Session.
- **A set whose Tools change between Turns** is a `ToolSource`, installed with `gotato.WithToolSource`; see [the extension points](extension-points.md).
- **For an MCP server** you do not write the set: `adapter/mcp` supplies it. See [Install](install.md).

## Models

A Model is one method: `Stream(ctx, ModelRequest) (ModelStream, error)`. The provider adapters in `gateway` implement it for OpenAI Chat Completions, OpenAI Responses and Anthropic Messages. You write your own Model to reach a provider they do not cover, or you wrap one.

### A Model wrapper

A wrapper holds another Model and passes the call on. It sees every request and every event, so it can log, count, trace, or rewrite.

```go
// Logged wraps a Model and sees every request and every event.
type Logged struct {
	Inner gotato.Model
	Log   func(string)
}

func (m Logged) Stream(ctx context.Context, req gotato.ModelRequest) (gotato.ModelStream, error) {
	m.Log(fmt.Sprintf("request: %d messages, %d tools", len(req.Messages), len(req.Tools)))
	stream, err := m.Inner.Stream(ctx, req) // pass the whole request on, unchanged
	if err != nil {
		return nil, err
	}
	return &loggedStream{ModelStream: stream, log: m.Log}, nil
}

type loggedStream struct {
	gotato.ModelStream
	log func(string)
}

func (s *loggedStream) Recv(ctx context.Context) (gotato.ModelEvent, error) {
	ev, err := s.ModelStream.Recv(ctx)
	if err == nil {
		s.log("event: " + string(ev.Kind))
	}
	return ev, err
}

// Close is always called, also when the Session stops reading before io.EOF.
func (s *loggedStream) Close() error {
	s.log("closed")
	return s.ModelStream.Close()
}
```

Used as `gotato.WithModel(Logged{Inner: model, Log: ...})`, one prompt logs `request: 1 messages, 0 tools | event: text_delta | event: done | closed`.

What a Model keeps to:

- **One goroutine reads a `ModelStream`.** `Recv` returns the next `ModelEvent` and `io.EOF` after the last. The caller always calls `Close`.
- **A reply is a stream** of `text_delta` and `reasoning_delta` events and `tool_call` events, then `usage`, then `done` with a `StopReason`. The Session may stop reading at `done`, so do your closing work in `Close`, not on `io.EOF`.
- **Honor `ctx`.** It ends the call, and a Model that ignores it holds the Run.
- **A wrapper passes the whole `ModelRequest` on.** Besides `Messages` and `Tools` it carries `SystemInstructions`, `Config` (sampling and reasoning hints), `CacheBreakpoints` and `CacheKey` (prompt-cache hints), and `LoadedTools` and `UnloadedTools` (the Tool catalog). A wrapper that copies only the fields it knows silently drops the rest. Change a field by copying the request, not by building a new one.

### Failures that say what to do

Gotato has no capability list for a Model. What a Model can tell the Session about a failure, it says with a method on the error it returns:

| Method on the error | What Gotato does | Resulting code |
|---|---|---|
| `Retryable() bool` returning true | marks the failure retryable: the same request, sent again later, may succeed | `model_failure`, retryable |
| `ContextOverflow() bool` returning true | treats the request as too long. When the refusal comes before any output, the Session builds a shorter Context and calls once more in that Turn | `context_overflow` |
| a `*gotato.RuntimeError` with `CodeNotSupported` or `CodeModelProtocolFailure` | keeps your code as it is | as given |
| any other error | a failure of the Model | `model_failure`, not retryable |

```go
// RateLimited is a Model failure that says it is worth retrying.
type RateLimited struct{}

func (RateLimited) Error() string   { return "provider: rate limited" }
func (RateLimited) Retryable() bool { return true }

// TooLong is a Model failure that says the request did not fit.
type TooLong struct{}

func (TooLong) Error() string         { return "provider: prompt too long" }
func (TooLong) ContextOverflow() bool { return true }
```

A Model that retries inside itself, as the gateway providers do, calls `gotato.ReportModelRetry(ctx, ...)` before it waits, so the Session stores a `ModelRetried` Event and discards the reply so far.

A context error is the caller's only when the Run's context really ended; one the Model produced itself (an HTTP client timeout) is a retryable `model_failure`.

## Tests

Test a Tool or a Model against a real Session, with the model played by `testkit`. Nothing here needs a network or a key. [Test with fakes](test-with-fakes.md) has the whole set of fakes and when to pick each.

### Test with scripted fakes

`testkit.NewFakeModel` takes one `Script` per model call. `testkit.ToolCalls` is a script that asks for tool calls, and `testkit.Text` one that answers. The Session runs your Tool for real. `testkit.ToolResults(request)` reads back what a request told the model about the Tool calls, so you assert on what the model would have read.

The imports for the tests below:

```go
import (
	"context"
	"slices"
	"strings"
	"testing"

	"example.com/yourapp/kit"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/testkit"
)
```

A Tool type, with one call that works and one that names a ticket that is not there:

```go
func TestCloseTicket(t *testing.T) {
	ctx := context.Background()
	tool := &kit.CloseTicket{Tickets: map[string]bool{"T-1": true}}
	// The model asks for two tickets: one exists, one does not.
	model := testkit.NewFakeModel(
		testkit.ToolCalls(
			gotato.ToolCall{ID: "c1", ToolID: "close_ticket", Arguments: []byte(`{"id":"T-1"}`)},
			gotato.ToolCall{ID: "c2", ToolID: "close_ticket", Arguments: []byte(`{"id":"T-9"}`)},
		),
		testkit.Text("closed T-1; T-9 does not exist"),
	)
	s, err := gotato.NewSession(ctx, gotato.WithModel(model), gotato.WithTools(tool))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)

	if _, err := s.Prompt(ctx, gotato.UserMessage("close T-1 and T-9")); err != nil {
		t.Fatal(err)
	}

	// What the second model call was told about the two tool calls.
	got := testkit.ToolResults(model.Requests()[1])
	want := []string{"closed T-1", "there is no ticket T-9"}
	if !slices.Equal(got, want) {
		t.Fatalf("tool results = %q, want %q", got, want)
	}
	if tool.Tickets["T-1"] {
		t.Fatal("T-1 is still open")
	}
}
```

The schema turns away bad arguments before the Tool runs, and the Run goes on:

```go
func TestSchemaRefusesBadArguments(t *testing.T) {
	ctx := context.Background()
	weather, err := kit.WeatherTool()
	if err != nil {
		t.Fatal(err)
	}
	model := testkit.NewFakeModel(
		testkit.ToolCalls(gotato.ToolCall{ID: "c1", ToolID: "weather", Arguments: []byte(`{"city":5}`)}),
		testkit.Text("sorry"),
	)
	s, err := gotato.NewSession(ctx, gotato.WithModel(model), gotato.WithTools(weather))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)

	if _, err := s.Prompt(ctx, gotato.UserMessage("weather?")); err != nil {
		t.Fatal(err)
	}
	// The Run went on: the model read the refusal and answered.
	got := testkit.ToolResults(model.Requests()[1])
	if len(got) != 1 || !strings.Contains(got[0], "arguments.city must be string") {
		t.Fatalf("tool results = %q", got)
	}
}
```

The Effect cap hides a Tool:

```go
func TestEffectCapHidesTool(t *testing.T) {
	ctx := context.Background()
	tool := &kit.CloseTicket{Tickets: map[string]bool{}}
	s, err := gotato.NewSession(ctx,
		gotato.WithModel(testkit.NewFakeModel(testkit.Text("ok"))),
		gotato.WithTools(tool),
		gotato.WithMaxEffect(gotato.ToolEffectRead),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	if n := len(s.Tools()); n != 0 {
		t.Fatalf("%d tools visible under a read cap, want 0", n)
	}
}
```

A ToolSet, installed active, under its qualified ID:

```go
func TestToolSetIDsAreQualified(t *testing.T) {
	ctx := context.Background()
	set := kit.TicketTools{Close: &kit.CloseTicket{Tickets: map[string]bool{"T-1": true}}}
	model := testkit.NewFakeModel(
		testkit.ToolCalls(gotato.ToolCall{ID: "c1", ToolID: "tickets.close_ticket", Arguments: []byte(`{"id":"T-1"}`)}),
		testkit.Text("done"),
	)
	// Active: the Model sees the set's Tools from the first Turn.
	s, err := gotato.NewSession(ctx, gotato.WithModel(model), gotato.WithActiveToolSet(set))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)

	if _, err := s.Prompt(ctx, gotato.UserMessage("close T-1")); err != nil {
		t.Fatal(err)
	}
	if got := testkit.ToolResults(model.Requests()[1]); !slices.Equal(got, []string{"closed T-1"}) {
		t.Fatalf("tool results = %q", got)
	}
}
```

A Model wrapper passes the request on and sees the events:

```go
func TestLoggedPassesTheRequestOn(t *testing.T) {
	ctx := context.Background()
	inner := testkit.NewFakeModel(testkit.Text("hi"))
	var lines []string
	logged := kit.Logged{Inner: inner, Log: func(l string) { lines = append(lines, l) }}
	s, err := gotato.NewSession(ctx, gotato.WithModel(logged), gotato.WithInstruction("be brief"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	if _, err := s.Prompt(ctx, gotato.UserMessage("hello")); err != nil {
		t.Fatal(err)
	}
	if req, _ := inner.LastRequest(); req.SystemInstructions != "be brief" {
		t.Fatalf("the inner model saw instruction %q", req.SystemInstructions)
	}
	if got := strings.Join(lines, "|"); got != "request: 1 messages, 0 tools|event: text_delta|event: done|closed" {
		t.Fatalf("log = %s", got)
	}
}
```

A `FakeModel` with `Err` set stands in for a failing provider, and the failures keep the meaning you gave them:

```go
func TestFailuresKeepTheirMeaning(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		failure   error
		code      gotato.ErrorCode
		retryable bool
	}{
		{kit.RateLimited{}, gotato.CodeModelFailure, true},
		{kit.TooLong{}, gotato.CodeContextOverflow, false},
	} {
		model := testkit.NewFakeModel()
		model.Err = tc.failure // returned by Stream instead of a script
		s, err := gotato.NewSession(ctx, gotato.WithModel(model))
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.Prompt(ctx, gotato.UserMessage("hi"))
		s.Close(ctx)
		if !gotato.IsCode(err, tc.code) || gotato.Retryable(err) != tc.retryable {
			t.Fatalf("%T: err = %v, want code %s retryable %v", tc.failure, err, tc.code, tc.retryable)
		}
	}
}
```

- **A FakeModel repeats its last Script** once the list ends, so a loop always finishes. `testkit.NewReplayModel` fails instead, when you want a call too many to be a test failure.
- **`FakeTool`** (`testkit.NewFakeTool`) is the reverse: a Tool with a fixed result that records its `ToolUse`s, for testing a Session or an Agent rather than a Tool.
- **Run them** with `go test -race ./...`.

## Next

- Where to put more behavior around a Run: [the extension points](extension-points.md).
- Keep Sessions somewhere of your own: [Write a Store](write-a-store.md).
- Everything `testkit` offers: [Test with fakes](test-with-fakes.md).
