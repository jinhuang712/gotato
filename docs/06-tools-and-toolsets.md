# Tools and ToolSets

**Status:** Draft

> Tools give an Agent access to the systems that make its answers useful.

## 1. The simple path

A Go function or existing service becomes an Agent capability through a Tool adapter:

```text
Go function / service
        ↓
Tool adapter
        ↓
Agent Core
        ↓
Model-visible Tool
```

The first Agent only needs individual Tools. ToolSets are optional grouping and discovery for larger capability surfaces.

## 2. Tool

A Tool is one model-callable operation:

```go
type Tool interface {
    Spec() ToolSpec
    Execute(context.Context, ToolUse, ToolProgress) (ToolResult, error)
}
```

Its specification contains a stable identity, description, input Schema, result expectations, and execution policy. Its executor receives Core values rather than provider or protocol types.

Typed function helpers should infer ordinary Go input and output types where possible, so a developer can add a Tool without manually writing a protocol schema for every function.

## 3. ToolSet

A ToolSet groups related operations:

```text
grafana
  ├── view_dashboard
  ├── edit_panel
  └── refresh
```

A ToolSet resolves deterministic concrete Tools when activated or inspected. External dependencies and credentials remain in the adapter.

ToolSets can support staged capability discovery:

```text
Model sees domains
  grafana · github · database
        ↓ activate grafana
Model sees concrete operations
  grafana.view_dashboard · grafana.edit_panel · ...
```

Staged discovery is an advanced capability, not part of the minimum Agent setup.

## 4. Identity and visibility

Core identity is:

```text
ToolSetName + "." + ToolName
```

Root Tools use a configured namespace and remain visible. Active ToolSets expose concrete Tools in deterministic order. Provider adapters may encode names but must preserve a reversible mapping to Core identity.

## 5. Lifecycle

```text
Tool Call
  → assemble complete JSON
  → resolve Tool
  → validate Schema
  → before hooks
  → execute at most once
  → after hooks
  → finalize Result
  → commit Tool Result
```

Blocked, invalid, and failed outcomes retain whether execution occurred. A Tool execution error normally becomes a failed Tool Result so the Model can continue.

## 6. Construction

Agent construction validates non-nil implementations, unique names, qualified IDs, valid Schemas, deterministic ordering, and visibility bounds before a Run is admitted. Dynamic ToolSet failure must not corrupt existing Core state.

## 7. Parallel batches

Preflight is source ordered. Execution may be sequential or bounded parallel. Completion Events reflect actual completion; Tool Result Messages commit in assistant source order. Every admitted Tool settles before the Turn ends.

Tool concurrency is local to one Agent Run. It does not allow a second Prompt to mutate the same Agent concurrently.

## 8. Progress and bounds

Tool progress is optional and coalescable. Final results are authoritative. Core enforces bounds for progress bytes, progress updates, result bytes, and metadata. Overflow never creates an unbounded Model context.

## 9. Adapter ownership

A Tool adapter owns authentication, external timeout mapping, protocol translation, and private diagnostics. Agent Core owns Tool identity, validation, cancellation, invocation boundaries, Events, and conversation commitment.

## 10. Embedded and Hosted

In Embedded mode, an application installs Tools directly on a Core Agent. In Hosted mode, a Host creates the Agent and delivers Tool Events through its protocol adapter. The Tool contract is identical in both modes.
