# Tools and ToolSets

**Status:** Draft

> **Tools extend Agent Core; adapters connect them to application systems. ToolSets stage capability discovery.**

## 1. Capability path

```text
Application system
   ↓
Tool / ToolSet adapter
   ↓
Agent definition
   ↓
Agent Core
   ↓
Model-visible Tool specifications
```

Database, Redis, HTTP, gRPC, MCP, workflow, sandbox, and remote Agent integrations are application or adapter concerns. Agent Core owns the stable Tool lifecycle.

## 2. Tool

A Tool is one model-callable operation:

```go
type Tool interface {
    Spec() ToolSpec
    Execute(context.Context, ToolUse, ToolProgress) (ToolResult, error)
}
```

Its specification includes stable identity, description, input Schema, result expectations, and execution policy. Its executor receives a validated Core ToolUse, not provider or Protobuf types.

## 3. ToolSet

A ToolSet groups related operations under one capability domain:

```text
grafana
  ├── view_dashboard
  ├── edit_panel
  └── refresh
```

A ToolSet resolves deterministic concrete Tools when activated or inspected. Its external dependencies and credentials belong to the adapter.

## 4. Staged discovery

```text
Model sees capability domains
  grafana · github · database
        ↓ activate grafana
Model sees concrete operations
  grafana.view_dashboard · grafana.edit_panel · ...
```

The built-in activation Tool is implemented through the ordinary Tool lifecycle. Activation is committed between Turns and affects the next Model request.

## 5. Identity and visibility

Core identity is:

```text
ToolSetName + "." + ToolName
```

Individual root Tools use one configured root namespace and remain visible. Active ToolSets expose concrete Tools in deterministic order. Provider adapters may encode names and must preserve a reversible mapping to Core identity.

## 6. Lifecycle

```text
Tool Call
  → assemble complete JSON
  → resolve Tool
  → validate Schema
  → Pre-Tool-Use
  → execute at most once
  → Post-Tool-Use
  → finalize Result
  → commit Tool Result Message
```

Blocked, invalid, and failed outcomes retain whether execution occurred. A Tool execution error normally becomes a failed Tool Result so the Model can continue.

## 7. Construction

Agent construction validates non-nil implementations, unique Tool and ToolSet names, qualified IDs, valid Schemas, deterministic ordering, and visibility bounds before a Run is admitted. Dynamic ToolSet failure must not corrupt existing Core state.

## 8. Parallel batches

Preflight is source ordered. Execution may be sequential or bounded parallel. Completion Events reflect actual completion; Tool Result Messages commit in assistant source order. Every admitted Tool settles before `turn_end`.

## 9. Progress and bounds

Tool progress is optional and coalescable. Final results are authoritative. Core enforces bounds for progress bytes, progress updates, result bytes, and metadata. Overflow never creates an unbounded Model context.

## 10. Adapter ownership

An adapter owns protocol translation, authentication, external timeout mapping, and private diagnostics. Agent Core owns Tool identity, validation, cancellation, invocation boundaries, Events, and transcript commitment.

## 11. Embedded and hosted use

In Embedded mode, an application installs Tools directly on a Core Agent. In Hosted mode, a Host creates the Agent from a factory and projects Tool Events through transport. The Tool contract is identical in both modes.
