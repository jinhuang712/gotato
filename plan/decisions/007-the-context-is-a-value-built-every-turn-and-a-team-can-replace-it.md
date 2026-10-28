# 007. The Context is a value built every Turn, which a team can replace and inspect

Decided: 2026-10-16    Status: current

## The problem
The model must see a view that fits its window, and what belongs in that view depends on the application's data. A request that is the history sent as it is cannot be bounded or inspected. A Run can also outgrow the window between two Turns, and a request can be refused as too long, so the view has to be able to change before any model call, not only when a Run starts.

## The choice
The Transcript and the Context are separate values. A `ContextBuilder` builds the Context from the Transcript on every Turn, so what the model sees is chosen, laid out and inspected. Gotato ships full history and Compaction; retrieval and windowing are a team's own `ContextBuilder`. One hook, a `TurnPreparer`, runs on the Session's goroutine before every model call with no Tool call open. Compaction is its first use, and the policy (the ceiling, the idle time, the refusal) stays in the compactor. Inspecting the Context runs the code a Turn runs.

## Not chosen
- Sending the history to the model as it is: nothing to build, but nothing to choose, bound or inspect.
- Built-in retrieval or memory search: it depends on the application's data, which Gotato does not know.
- Preparing a Run only when it starts: one fewer hook, but a Run can outgrow the window between two Turns.
- A separate preview of the Context: the same answer, but a second code path that can drift from what a Turn sends.

## Consequences
A team can see what the model would receive now without calling it, and can replace the builder without touching the Loop. The hook is one more stage in the Loop, run before every model call. A team that wants retrieval writes its own builder.
