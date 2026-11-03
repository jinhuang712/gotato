# Add checks and stops to the Loop

> [Wiki](../README.md) › [How-to](../README.md#how-to) › Add checks and stops to the Loop

By the end of this page you have refused a tool call, changed its result, or ended a Run after a number of Turns, with an Extension. You need the [first page](../tutorials/getting-started.md).

## Add behavior at the six stages

An Extension is a value with one or more stage methods, installed with `gotato.WithExtension`. One value may implement several stages. The stages, in the order a Turn meets them:

| Stage | Method | Runs |
|---|---|---|
| `TurnPreparer` | `PrepareTurn` | before each model call; the one safe point to rewrite the Transcript |
| `ContextTransformer` | `Transform` | on the builder's Messages |
| `MessageConverter` | `Convert` | after the transformers, to fit one provider |
| `PreToolUse` | `Before` | before a tool runs; can block it |
| `PostToolUse` | `After` | after a tool, in reverse order; can change the result |
| `TurnStopper` | `Stop` | after a Turn; can end the Run |

Two small ones, and how they are installed:

```go
// blocker is an Extension with one stage, PreToolUse. It lets every call through.
type blocker struct{}

func (blocker) Before(_ context.Context, use gotato.ToolUse) (gotato.PreToolDecision, error) {
	return gotato.PreToolDecision{}, nil // return {Block: true, Reason: "..."} to refuse
}

// stopAfter is an Extension with one stage, TurnStopper. It ends the Run after N Turns.
type stopAfter struct{ turns int }

func (s stopAfter) Stop(_ context.Context, t gotato.TurnSnapshot) (gotato.StopDecision, error) {
	return gotato.StopDecision{Stop: int(t.Turn) >= s.turns, Reason: "turn cap"}, nil
}

s, err := gotato.NewSession(ctx,
	gotato.WithModel(model),
	gotato.WithExtension(blocker{}),
	gotato.WithExtension(stopAfter{turns: 8}),
)
```

`WithExtension` fails when a value has a method named like a stage (`Before`, `Stop`, ...) with the wrong signature, so a typo does not pass silently. A failing extension ends the Run, unless it also implements `Advisory() bool` and returns true: then the failure is recorded as an `extension_failed` Event.

Through the Runner, pass extensions in `profile.AgentProfile.Extensions`; the Runner installs each with `gotato.WithExtension`, after its own.

## See also

- [How the Loop runs extensions](../../specs/session.md)
- [Extensions, as the user sees them](../../FEATURES.md#f11-extensions)
- [Ask a person before a step](ask-a-person-before-a-step.md)
