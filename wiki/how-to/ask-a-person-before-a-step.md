# Ask a person before a step

> [Wiki](../README.md) › [How-to](../README.md#how-to) › Ask a person before a step

By the end of this page a step that needs a person waits for one, or is handed over as a ticket. You need [Add checks and stops to the Loop](add-checks-and-stops.md).

## Ask a person, or hand the step over

Gotato has no built-in approval and no waiting state in the Loop ([decision 005](../../plan/decisions/005-no-approval-system.md)). Two ways to ask a person before a step:

**Ask inside `PreToolUse`.** `Before` runs for every call of a Turn, one after another, before any executor of that Turn starts. It gets a `ToolUse`: `RunID`, `Turn`, `CallID`, `ToolID`, `Arguments`, `SourceIndex` and `Effect`. It has no Session id.

- A wait inside `Before` ends with the Run's context, or at `RunTimeout`. `ToolCallDeadline` does not bound it, because no tool call has started yet.
- A `Before` that returns an error ends the Run with `CodeExtensionFailure`. To fail closed without ending the Run (a timeout, a "no"), return `PreToolDecision{Block: true, Reason: "..."}`: the model sees a blocked result and goes on.
- A blocked call has a `tool_ended` Event and no `tool_started`. This is on purpose: `tool_started` says a tool began, and recovery relies on that.

**Hand the step to a person from a tool.** A tool that opens a ticket, as decision [005: No approval system](../../plan/decisions/005-no-approval-system.md) says, and returns at once. Use this when the wait is longer than a Run should live.

## See also

- [Why there is no built-in approval](../../plan/decisions/005-no-approval-system.md)
- [Get a structured answer](get-a-structured-answer.md)
