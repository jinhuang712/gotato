# 05. Tools and ToolSets

**Status:** Draft

> Tool is the execution unit. ToolSet is the capability-discovery unit. The Runtime fixes invocation, validation, commitment, and identity; applications provide the operation.

## 1. Tool contracts

A Tool has a stable specification and one executor:

```go
type Tool interface {
    Spec() ToolSpec
    Execute(context.Context, ToolUse, ToolProgress) (ToolResult, error)
}

type ToolSpec struct {
    ID          string
    Name        string
    Description string
    InputSchema []byte
    OutputSchema []byte
    Sequential  bool
    Metadata    map[string]string
}
```

`Spec().Name` MUST be non-empty and contain only the configured portable name character set. `Spec().InputSchema` MUST be valid JSON Schema in the supported subset. A Tool ID MUST be unique after qualification.

The executor receives validated arguments through `ToolUse`. It MUST NOT be asked to parse provider-specific Tool Call objects or Protobuf messages.

## 2. ToolSet contracts

```go
type ToolSet interface {
    Spec() ToolSetSpec
    Tools(context.Context) ([]Tool, error)
}

type ToolSetSpec struct {
    Name        ToolSetName
    Description string
    Metadata    map[string]string
}
```

A ToolSet name MUST be stable, non-empty, and unique within an Agent definition. `Tools` MUST return a deterministic collection: repeated resolution with equivalent configuration produces the same qualified IDs and order. A ToolSet error does not partially register the returned collection.

## 3. Tool Use and outcome shapes

```go
type ToolUse struct {
    RunID         RunID
    Turn          TurnNumber
    CallID        ToolCallID
    QualifiedID   string
    ArgumentsJSON []byte
    SourceIndex   uint32
    Status        ToolUseStatus
    Executed      bool
    Result        *ToolResult
}

type ToolProgress struct {
    Emit func(context.Context, ToolProgressUpdate) error
}

type ToolProgressUpdate struct {
    Text     string
    Metadata map[string]string
}

type ToolResultStatus string

const (
    ToolSucceeded  ToolResultStatus = "succeeded"
    ToolFailed     ToolResultStatus = "failed"
    ToolBlocked    ToolResultStatus = "blocked"
    ToolCancelled  ToolResultStatus = "cancelled"
)

type ToolResult struct {
    CallID    ToolCallID
    Status    ToolResultStatus
    Content   []ContentPart
    Metadata  map[string]string
    SafeError string
    Executed  bool
}
```

The Runtime owns `ToolUse`; a Tool MUST treat its fields as immutable. A progress reporter is bounded and Context-aware. A Tool MUST NOT retain it after `Execute` returns.

A final Tool Result MUST state whether execution occurred and MUST be safe to put into the Model context. Private causes, credentials, stack traces, and unrestricted external payloads stay in application diagnostics.

## 4. Construction validation

Agent construction MUST eagerly validate:

```text
non-nil Tool and ToolSet implementations
unique local Tool names within a ToolSet
unique ToolSet names
unique qualified Tool IDs
valid input and output Schemas
stable ordering
root namespace collisions
activation and visibility bounds
```

Construction failure MUST happen before a Run is admitted. Dynamic ToolSet failures occur at activation or resolution and MUST NOT corrupt previously committed Agent state.

## 5. Qualified identity

Core identity is:

```text
ToolSetName + "." + ToolName
```

Examples:

```text
grafana.view_dashboard
grafana.edit_panel
github.view_repository
```

Individual Tools use one explicit root namespace and remain visible on every Model request. Provider adapters MAY encode a qualified ID to satisfy provider syntax, but the mapping MUST be stable, reversible within that request, and retained in Event correlation.

## 6. Visibility state

The Runtime maintains three sets:

```text
registered ToolSets   configured capability domains
active ToolSets       domains committed into Agent state
visible Tools         concrete specs sent to the next Model request
```

Individual root Tools are always visible. Active ToolSets resolve their concrete Tools for each Turn. Inactive ToolSets appear only through the activation Tool when at least one inactive domain remains and the activation Tool itself is within visibility limits.

The visible collection MUST have deterministic order and MUST obey the configured active ToolSet and Tool limits.

## 7. Activation Tool

The built-in activation Tool has the conceptual specification:

```text
name: activate_toolset
description: activate one registered inactive ToolSet
input schema: object with required "name"
name schema: enum of currently inactive ToolSet names, sorted lexicographically
```

The name list is generated from Agent state for the current Turn. A call MUST:

1. parse and validate the name;
2. resolve it against registered ToolSets;
3. run Pre-Tool-Use;
4. commit activation at the Tool batch boundary;
5. run Post-Tool-Use;
6. emit activation and Tool lifecycle Events;
7. expose concrete Tools on the next Model request.

Activation is idempotent. It MUST NOT activate more than one ToolSet per Tool Use, exceed visibility limits, or make a partially resolved ToolSet visible.

## 8. Argument assembly and validation

The Runtime assembles complete JSON arguments from Model argument deltas before resolution. It MUST then:

```text
reject empty or malformed JSON
resolve qualified Tool ID
validate against InputSchema
normalize only according to the Schema contract
create immutable ToolUse
run Pre-Tool-Use
```

Schema validation errors do not invoke the executor. The Model-facing error MUST include a safe path to the invalid field and MUST NOT include secrets or arbitrary provider payloads.

The Runtime MUST NOT silently fill missing required fields, coerce an invalid type, or execute a Tool on a JSON prefix.

## 9. Tool lifecycle

One Tool Use follows:

```text
Tool Call
  → complete argument assembly
  → Tool resolution
  → Schema validation
  → Pre-Tool-Use chain
  → executor at most once
  → Post-Tool-Use chain
  → final Tool Result
  → Tool Result Message commit
```

A blocked use follows the same lifecycle except the executor is skipped. Post-Tool-Use receives both executed and blocked outcomes.

A Tool panic is recovered at the Tool boundary and becomes a failed Tool Result or terminal Runtime error according to the failure policy. The panic MUST NOT escape into an unowned goroutine.

## 10. Error conversion

Tool executor errors normally become:

```text
Executed = true
failed Tool Result
safe Model-facing error
canonical tool_execution_end
next Model Turn may continue
```

Tool resolution, argument validation, and blocked policy outcomes have `Executed = false`. Runtime protocol failures, corrupted Tool identity, and invariant failures terminate the Run rather than pretending to be an external Tool failure.

## 11. Parallel batches

Preflight is always source ordered. Execution may be sequential or bounded parallel. Completion Events reflect actual completion order; transcript Tool Result Messages are committed in source order.

A positive concurrency bound MUST be configured for parallel mode. A batch coordinator MUST wait for every admitted Tool to settle or be cancelled before `turn_end`. It MUST preserve one final outcome per Tool Call and MUST NOT execute a Call twice.

## 12. Progress and result bounds

Progress is observational and MAY be coalesced by a consumer. Final results are authoritative and MUST remain available to the Model even when progress volume is limited.

The Runtime MUST enforce configured bounds for:

```text
progress bytes
progress update count
final result bytes
metadata size
```

A progress overflow MAY truncate or coalesce updates. A final result overflow produces a typed Tool limit outcome and never silently creates an unbounded Model context.

## 13. Composition helpers

The public construction surface SHOULD provide equivalents of:

```text
WithTool
WithTools
WithToolSet
WithToolSets
```

Typed-function helpers MAY derive JSON Schema and execution glue from Go types. Source generation MAY assemble ToolSets. Both mechanisms MUST retain explicit construction order and dependency visibility; package-global discovery is not part of the Core composition model.

## 14. Capability adapters

HTTP APIs, gRPC services, MCP servers, workflows, scripts, sandboxes, and remote Agents MAY implement Tool or ToolSet through adapters. The adapter owns authentication, protocol translation, external timeout mapping, and private diagnostics. Core sees only the Tool and ToolSet contracts above.
