# 08. Errors and Limits

**Status:** Draft

> Tool failures become reasoning input; runtime and Routine failures become typed outcomes.

## 1. Error categories

Core MUST expose typed categories for:

```text
invalid argument
invalid state / busy
Model failure
Model protocol failure
Tool resolution failure
Tool argument validation failure
Tool execution failure
Extension failure
Routine failure
limit exceeded
cancelled
deadline exceeded
internal invariant failure
```

Errors SHOULD support `errors.Is` and `errors.As`.

## 2. Tool and Run outcomes

A Tool execution error normally produces:

```text
failed Tool Result Message
Tool execution end Event
continued Model reasoning
```

Model protocol failures, corrupted runtime state, cancellation, deadlines, local-limit failures, and blocking Extension errors produce a terminal Run error.

## 3. Routine outcomes

A child Run result becomes a settled Routine Result:

```text
Completed  child Run completed
Failed     child Run returned a terminal error
Cancelled  Routine Context was cancelled
```

The parent Tool or Routine Group policy decides how that outcome affects parent reasoning and sibling Routines.

## 4. Diagnostics

Public error Messages MUST be safe for Model and transport exposure. Errors MAY retain an application-controlled diagnostic cause for logs and traces.

Panic recovery boundaries MUST exist around Tools, Extensions, subscribers, Routine tasks, and service callbacks.

## 5. Local limits

Phase one MUST support:

```text
Turns per Run
Tool Calls per Run
active ToolSets
visible Tools
parallel Tool executions
Tool Result bytes
Tool progress bytes or update count
Routines spawned per parent Run
concurrently active Routines
Routine nesting depth
```

It SHOULD support Run, Model-call, Tool-call, and child-Routine deadlines.

## 6. Limit behavior

When a limit is reached:

1. the Runtime stops admitting work governed by that limit;
2. it emits a typed failure Event;
3. it cancels active child work when required;
4. it returns a typed Tool, Routine, or Run outcome.

Progress-volume limits MAY truncate or coalesce optional updates while preserving final outcomes.

## 7. Service mapping

Service transports map Core categories to stable protocol errors. Shared quotas, billing, and fleet-wide admission remain service or Extension concerns.
