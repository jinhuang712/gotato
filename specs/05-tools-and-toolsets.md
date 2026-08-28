# 05. Tools and ToolSets

**Status:** Draft

> Tool is the Core execution unit. ToolSet is the staged capability-discovery unit.

## 1. Tool

```go
type Tool interface {
    Spec() ToolSpec
    Execute(context.Context, ToolUse, ToolProgress) (ToolResult, error)
}

type ToolSpec struct {
    ID string
    Name string
    Description string
    InputSchema []byte
    OutputSchema []byte
    Sequential bool
    Metadata map[string]string
}
```

A Tool has a stable name, valid supported JSON Schema, and one executor. It receives validated Core values and no provider or Protobuf objects.

## 2. ToolSet

```go
type ToolSet interface {
    Spec() ToolSetSpec
    Tools(context.Context) ([]Tool, error)
}
```

A ToolSet has a stable unique name and deterministic Tool resolution. Failure must not partially register the returned collection.

## 3. Identity and visibility

Qualified identity is:

```text
ToolSetName + "." + ToolName
```

Individual root Tools use a configured root namespace and remain visible. Active ToolSets expose concrete Tools. Inactive ToolSets appear through the activation Tool. Visible ordering is deterministic and obeys limits.

## 4. Activation

The activation Tool has one required `name` field whose inactive ToolSet enum is sorted lexicographically. Its execution is:

```text
assemble and validate
  → resolve inactive ToolSet
  → Pre-Tool-Use
  → commit activation at batch boundary
  → Post-Tool-Use
  → emit lifecycle Events
  → expose Tools on next Model request
```

Activation is idempotent, bounded, and cannot partially expose a ToolSet. It never changes visibility for the current Model request.

## 5. Construction validation

Core construction MUST reject nil implementations, duplicate ToolSet names, duplicate local names, duplicate qualified IDs, invalid Schemas, unstable ordering, namespace collisions, and incompatible visibility bounds before Run admission.

## 6. Argument pipeline

Core assembles complete JSON arguments before resolving a Tool:

```text
complete JSON → resolve → Schema validate → immutable ToolUse
```

Malformed or incomplete JSON never reaches an executor. Validation errors include safe field paths and do not expose arbitrary provider payloads. Core does not silently fill required fields or coerce invalid types.

## 7. Execution semantics

```text
Tool Call → resolve → validate → Pre chain
          → executor at most once
          → Post chain → final Result → transcript commit
```

Blocked uses skip the executor and have `Executed == false`. An invoked executor has `Executed == true` even if it fails or is cancelled. Tool errors normally become failed Tool Results for Model reasoning.

## 8. Parallel batches

Preflight is always source ordered. Execution may be sequential or bounded parallel. Completion Events use actual completion order; Tool Result Messages commit in assistant source order. Every admitted Tool has one final outcome before `turn_end`.

## 9. Progress and result bounds

Progress is optional and coalescable. Core enforces progress bytes, update count, result bytes, and metadata bounds. Progress overflow may truncate/coalesce; final result overflow is a typed Tool limit and never creates unbounded Model context.

## 10. Adapters

HTTP, gRPC, MCP, database, Redis, workflow, sandbox, and remote Agent adapters implement Tool or ToolSet. Adapters own authentication, protocol translation, external timeout mapping, and private diagnostics. Core owns identity, validation, cancellation, invocation, Events, and commitment.

## 11. Composition

Construction helpers such as `WithTool`, `WithTools`, `WithToolSet`, and `WithToolSets` may reduce boilerplate. Typed helpers and generation are allowed only when dependency and ordering remain explicit; package-global discovery is not part of Core composition.
