# Tools and ToolSets

**Status:** Draft

> A Tool is an operation. A ToolSet is a capability domain exposed through a hosted Agent definition.

## 1. Capability path

```text
Application capability
        ↓
Tool / ToolSet adapter
        ↓
Agent definition
        ↓
Agent factory
        ↓
Runtime-visible Tool specifications
        ↓
Model selection and Tool Use
```

Applications own business APIs and credentials. Gotato owns the model-facing capability contract and deterministic Tool lifecycle.

## 2. Tool

A Tool exposes one model-callable operation. Its runtime contract is conceptually:

```go
type Tool interface {
    Spec() ToolSpec
    Execute(ctx context.Context, use ToolUse, progress ToolProgress) (ToolResult, error)
}
```

A Tool specification contains:

```text
stable identity
description
input JSON Schema
portable result expectations
```

Execution may call local Go code or an external system through an adapter. Runtime Tool types remain independent of Protobuf and provider SDK types.

## 3. ToolSet

A ToolSet groups related Tools behind one capability description:

```go
type ToolSet interface {
    Spec() ToolSetSpec
    Tools(ctx context.Context) ([]Tool, error)
}
```

```text
grafana
  ├── view_dashboard
  ├── edit_panel
  ├── probe
  ├── refresh
  └── sync
```

Applications compose Agent definitions at the ToolSet level:

```text
incident Agent
  Model
  grafana ToolSet
  logs ToolSet
  repository ToolSet
  runtime limits
```

The Agent factory installs this definition when the service resolves the named Agent.

## 4. Two-stage model choice

ToolSets turn a flat operation-selection problem into two focused decisions:

```text
Stage A: capability discovery

Model
 ├── grafana
 ├── github
 ├── kubernetes
 └── database
       │
       ▼ activate grafana

Stage B: operation selection

Model
 ├── grafana.view_dashboard
 ├── grafana.edit_panel
 ├── grafana.probe
 ├── grafana.refresh
 └── grafana.sync
```

This reduces Model context size and operation-selection entropy while preserving explicit Tool identity.

## 5. Registration, activation, and visibility

```text
┌──────────────────────┐
│ Registered ToolSets  │  all domains configured on the Agent
└──────────┬───────────┘
           │ activate
           ▼
┌──────────────────────┐
│ Active ToolSets      │  domains selected in Agent state
└──────────┬───────────┘
           │ resolve for Turn
           ▼
┌──────────────────────┐
│ Visible Tools        │  specifications sent to the Model
└──────────────────────┘
```

Individually configured Tools remain visible. ToolSet activation changes visibility between Model Turns and remains in the conversation Agent until reset or explicit state change.

The service owns how that Agent is retained; the runtime owns activation state consistency.

## 6. Activation protocol

When inactive ToolSets exist, the Runtime exposes a built-in activation Tool containing their stable names and descriptions:

```text
activate_toolset(name="grafana")
```

A successful activation follows the ordinary Tool path:

```text
Tool Call
  → resolve and validate
  → Pre-Tool-Use
  → activation state transition
  → Post-Tool-Use
  → Tool Result
  → next Model Turn sees concrete Tools
```

Activation is deterministic, idempotent, bounded, and observable through canonical Events.

## 7. Identity and encoding

Core uses qualified Tool identity:

```text
grafana.view_dashboard
grafana.edit_panel
github.view_repository
```

Provider adapters may encode names to satisfy provider restrictions:

```text
grafana.view_dashboard ↔ grafana_view_dashboard
```

The mapping is stable and reversible within a Model request. Service Event projection preserves the canonical qualified identity even when the provider uses an encoded name.

## 8. Construction validation

Agent construction validates the complete capability composition:

```text
Tool and ToolSet names
input Schemas
duplicate ToolSet names
duplicate qualified Tool identities
deterministic ordering
non-nil implementations
visibility bounds
```

Invalid static composition fails before the service admits a Run for that Agent instance.

## 9. Tool Use and outcomes

```text
Model Tool Call
      ↓
complete argument assembly
      ↓
Tool resolution and Schema validation
      ↓
Pre-Tool-Use
      ↓
Tool executor at most once
      ↓
Post-Tool-Use
      ↓
final Tool outcome
      ↓
Tool Result Message and Events
```

A Tool outcome records whether execution occurred, its model-facing content, typed metadata, failure details safe for the Model, and any termination hint.

Tool execution errors normally become failed Tool Results so the Model can reason about them. Runtime protocol and invariant failures terminate the Run.

## 10. Progress and network projection

A Tool may report bounded progress:

```text
Tool progress
    ↓
canonical Tool Event
    ↓
service Event projection
    ↓
bounded gRPC Event bridge
```

Optional progress can be coalesced for a slow client. The final Tool outcome and lifecycle order remain intact.

## 11. Parallel batches

```text
Assistant source order: A · B · C
Execution:              bounded concurrency
Completion Events:      actual completion order
Transcript Results:     A · B · C
```

This lets the service stream real progress while preserving deterministic Model context.

## 12. Capability adapters

Adapters can expose remote systems through the same contracts:

```text
HTTP API      → Tool or ToolSet
gRPC service  → Tool or ToolSet
MCP server    → ToolSet
workflow      → Tool
remote Agent  → Tool or Agent Routine
sandbox       → Tool execution adapter
```

Each adapter owns its protocol dependencies, authentication integration, and external failure mapping.

## 13. Go composition

Constructors and explicit options assemble Tools and ToolSets. Typed-function helpers can derive Schema and execution glue from Go input and output types. Source generation can assemble package-level ToolSets while retaining explicit dependencies.

The exact public Go surface is promoted from the runtime contracts used successfully by hosted Agents and direct consumers.
