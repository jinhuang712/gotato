# Test a tool and a model

> [Wiki](../README.md) › [How-to](../README.md#how-to) › Test a tool and a model

By the end of this page you have tested your Tool, your ToolSet and your Model wrapper against a real Session, with the model played by `testkit`. You need [Write a tool](write-a-tool.md), [Group tools into a ToolSet](group-and-supply-tools.md) and [Wrap a model](wrap-a-model.md).

Test a Tool or a Model against a real Session, with the model played by `testkit`. Nothing here needs a network or a key. [Test with fakes](test-with-fakes.md) has the whole set of fakes and when to pick each.

## Play the model with scripted fakes

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

## See also

- [Test with fakes](test-with-fakes.md)
- [Write a tool](write-a-tool.md)
- [Wrap a model](wrap-a-model.md)
