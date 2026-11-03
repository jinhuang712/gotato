# Write a tool

> [Wiki](../README.md) › [How-to](../README.md#how-to) › Write a tool

By the end of this page you have given an agent a tool of your own, as a function or as a type that holds state, with an Effect the Session can cap. You need the [first page](../tutorials/getting-started.md). Every snippet here was compiled and run as a test against the root module.

## Turn a function into a tool

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

## Give a tool an Effect

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

On the service side, `profile.AgentProfile` has a `MaxEffect` field with the same cap, so one Agent can be read-only and another not. A `PreToolUse` Extension can gate a single call by its Effect; see [the extension points](../reference/extension-points.md).

## Write a tool as a type

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
- **Honor `ctx`.** The Session cancels it when the Run is aborted or the call passes its deadline. At the deadline the Run stops waiting: the Model reads that the call timed out with its outcome unknown, and the Run goes on. A Tool that ignores `ctx` runs on in its goroutine until it returns, and its late result is dropped.
- **Thread safety.** A Turn may run its Tool calls in parallel. Set `ToolSpec.Sequential` for a Tool that must never run beside another Tool of the same Turn, and guard shared state in the Tool itself (the sketch above does not, because the example runs one call at a time).
- **A panic becomes a failed Tool Result,** and does not take the process down.
- **Install it** with `gotato.WithTools(tool)` on a Session, or in `profile.AgentProfile.Tools`.

## Tell the model why a call failed

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

## See also

- [Group tools into a ToolSet](group-and-supply-tools.md)
- [Test a tool and a model](test-a-tool-and-a-model.md)
- [How tools reach the model](../../specs/tools.md)
- [Tools, as the user sees them](../../FEATURES.md#f6-tools)
