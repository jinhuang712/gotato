# 023. A mistake the model can fix is a tool result, and every Run settles with a status and a cause

Decided: 2026-10-16    Status: current

## The problem
A failure inside a Run is not always the end of it: some the model can correct, others the Run cannot go on from. A caller must always learn how a Run ended, and why.

## The choice
An unknown tool or bad arguments go back to the model as a failed tool result, and the Run goes on. Every Run ends, in one of five ways: the model's final reply, its `ctx`, a `TurnStopper` a team writes, the compaction ceiling, or a failure. The core counts neither Turns nor tool calls. A model that keeps getting it wrong is bounded by the Run's deadline or a `TurnStopper`; a call that hangs is ended by `ModelCallDeadline` or `ToolCallDeadline`, and a Run that never settles by its `ctx`, with one Run deadline per Agent in a service (`service.Agent.RunTimeout`). Every Run settles with a status and a cause, and the cause names the part that failed: the model, the provider, a tool, the store, a limit, an extension, an abort or Gotato.

## Not chosen
- Failing the Run on a mistake the model can fix: a stricter loop, but one wrong argument would end the conversation's work.
- No end, left to the caller, or a count of Turns and tool calls kept by the core: one number cannot fit every job ([038](038-a-limit-guards-a-real-failure.md)).
- One generic error: less to learn, but the caller cannot tell a model failure from a store failure.

## Consequences
A Run always ends and says why, and no failure loses the conversation. Whether a failed request ran, and whether to retry, is [035](035-the-runtime-says-whether-an-error-ran-and-whether-to-retry.md).
