# 01. Core Domain

**Status:** Draft

> Agent owns state. Run owns one invocation. Turn owns one Model response and its Tool batch. Every identity, state transition, and committed value has an explicit owner.

This specification defines the runtime values shared by the Agent loop. Service and transport layers may add identifiers and projections, but they MUST NOT replace these values or change their ownership rules.

## 1. Identity types

The implementation SHOULD use distinct Go types for identifiers even when their wire representation is a string:

```go
type AgentName string
type ConversationKey string
type RunID string
type TurnNumber uint32
type MessageID string
type ToolCallID string
type ToolSetName string
type ToolName string
type RoutineID string

type RunStatus string
const (
    RunRunning          RunStatus = "running"
    RunCompleted        RunStatus = "completed"
    RunFailed           RunStatus = "failed"
    RunCancelled        RunStatus = "cancelled"
    RunDeadlineExceeded RunStatus = "deadline_exceeded"
    RunLimitExceeded    RunStatus = "limit_exceeded"
)

type ToolUseStatus string
const (
    ToolUseRunning   ToolUseStatus = "running"
    ToolUseBlocked   ToolUseStatus = "blocked"
    ToolUseSucceeded ToolUseStatus = "succeeded"
    ToolUseFailed     ToolUseStatus = "failed"
    ToolUseCancelled  ToolUseStatus = "cancelled"
)

type RoutineStatus string
const (
    RoutineQueued    RoutineStatus = "queued"
    RoutineRunning   RoutineStatus = "running"
    RoutineCompleted RoutineStatus = "completed"
    RoutineFailed    RoutineStatus = "failed"
    RoutineCancelled RoutineStatus = "cancelled"
)
```

The following rules apply:

- `RunID` MUST be non-empty, immutable, and unique for the process lifetime.
- `TurnNumber` MUST start at `1` and increase by one for each Model Turn in a Run.
- A `MessageID` MUST be unique within an Agent transcript; streamed updates for one Message use one ID.
- A `ToolCallID` MUST be unique within the assistant Message that produced it.
- A qualified Tool ID is `ToolSetName + "." + ToolName`; a root Tool uses the reserved root namespace selected by the Agent configuration.
- A `RoutineID` MUST be unique within its parent Run and MUST never be reused for a sibling Routine.
- Service request IDs, stream IDs, trace IDs, and provider IDs are additional correlation values. They MUST NOT be used in place of `RunID` or `TurnNumber`.

IDs are opaque. Consumers MUST NOT infer time, ordering, ownership, or retry identity from their textual representation.

## 2. Agent state

An Agent is a stateful runtime object with one serialized mutation owner:

```go
type AgentState struct {
    SystemInstructions string
    Messages           []Message
    RegisteredToolSets []ToolSetState
    ActiveToolSets     []ToolSetName
    Steering           []Message
    FollowUps          []Message
    Run                *ActiveRun
    Options            RuntimeOptions
}

type ActiveRun struct {
    RunID  RunID
    Status RunStatus
}
```

The exact Go field names MAY evolve, but the state MUST represent these values. A caller MUST NOT receive mutable internal slices, maps, or pointers through a snapshot.

An Agent has the following state machine:

```text
Idle ──Prompt/Continue──► Running
Idle ──Reset─────────────► Idle
Running ──terminal───────► Idle
Running ──Prompt/Continue► Busy error
Running ──Reset──────────► Invalid-state error
```

`Steer` and `FollowUp` are queue operations. They do not acquire a second Run and do not mutate committed transcript history until the loop reaches their defined boundary.

## 3. Run

A Run is one accepted `Prompt` or `Continue` operation:

```go
type RunResult struct {
    RunID        RunID
    Status       RunStatus
    FinalMessage *Message
    Usage        Usage
    Error        *RuntimeError
}
```

`RunResult` is immutable after execution settlement. `FinalMessage` is the last committed assistant Message when one exists. A Run that fails before an assistant Message MAY return a nil `FinalMessage`; it MUST still return its Run ID and terminal status.

The runtime MUST expose equivalent statuses:

```text
completed
failed
cancelled
deadline_exceeded
limit_exceeded
```

A Run lifecycle is:

```text
Created → Accepted → Running → Settling → Settled
```

`Created` is internal. `Accepted` means the Agent owns the Run and `agent_start` may be emitted. `Settling` begins after the loop has selected a terminal outcome and starts no new work. `Settled` means the terminal Event has been emitted and all awaited observers have returned.

A terminal Run MUST satisfy all of the following:

- exactly one terminal result is stored;
- exactly one `agent_end` Event is produced;
- no Model stream, Tool Use, Routine, queue continuation, or retry starts afterward;
- repeated waits return the same immutable result;
- cancellation of a settled Run is a no-op.

Execution settlement is distinct from remote delivery settlement. The runtime MUST NOT wait for a network consumer.

## 4. Turn

A Turn owns one Model request and the Tool batch produced by that request:

```go
type Turn struct {
    RunID       RunID
    Number      TurnNumber
    Assistant   Message
    ToolUses    []ToolUse
    StopReason  StopReason
    Usage       Usage
}
```

A Turn starts before context transformation and ends only after:

1. the assistant Message is committed;
2. every requested Tool Use has a finalized outcome; and
3. the Tool Result Messages are committed in assistant source order.

A Turn MUST NOT commit a Tool Result belonging to another Turn. A Turn number is never reused, including after an internal Model retry.

## 5. Tool Call and Tool Use

A Tool Call is Model output. A Tool Use is the Runtime-owned execution record:

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
```

The Runtime creates a Tool Use only after the complete argument JSON has been assembled, the Tool has been resolved, and Schema validation has passed. A blocked use has `Executed == false`; a failed executor has `Executed == true` and a failed result.

The executor MUST be invoked at most once for one Tool Use. A retry is a new explicitly identified Tool Use, not an invisible replay of the old one.

## 6. Snapshot contract

`StateSnapshot` MUST return a read-only value equivalent to:

```go
type AgentSnapshot struct {
    Messages          []Message
    RegisteredToolSets []ToolSetState
    ActiveToolSets    []ToolSetName
    Busy              bool
    ActiveRunID       *RunID
    QueuedSteering    int
    QueuedFollowUps   int
}
```

The snapshot MUST:

- preserve transcript order;
- report active ToolSets in deterministic order;
- copy all slices and maps, including nested Message content;
- remain valid after the Agent starts, settles, or resets another Run;
- omit mutable executor handles, Context values, provider clients, and internal locks.

## 7. State invariants

The following are Core invariants, not service policies:

- An accepted Prompt Message is committed before its first Model request.
- An assistant Message is committed before Pre-Tool-Use runs.
- A Tool Result references a Tool Call in the current assistant Message.
- Batch Tool Results are committed in assistant source order.
- ToolSet activation is committed between Turns and affects the next Model request.
- Steering and Follow-up acceptance order is preserved within each queue.
- Reset and Run mutation are mutually exclusive.
- A terminal Run starts no new work.
- A snapshot cannot mutate Agent state through aliasing.

## 8. Completion causes

A Run may settle because of:

```text
normal Model completion with no continuation
Tool or Extension termination decision
context cancellation
Run or child deadline
local limit exhaustion
fatal Model or protocol failure
runtime invariant failure
```

Tool execution failure is not automatically a Run completion cause. When the loop remains valid, it becomes a failed Tool Result and the Model receives it as reasoning input.

A transient Model failure MAY be retried inside the same Run only when the configured retry policy admits it. Any retry MUST finish before `agent_end`, MUST obey the Run Context and limits, and MUST NOT create a second terminal signal.

## 9. Routine relationship

An Agent Routine owns a child Run relationship:

```go
type RoutineRef struct {
    RoutineID  RoutineID
    Name       string
    ParentRun  RunID
    ChildRun   RunID
    Status     RoutineStatus
}
```

The child uses a distinct Agent instance and transcript. Parent and child share correlation, not mutable Agent state.

## 10. Service relationship

The service owns:

```text
external request and stream identity
Agent definition lookup
conversation routing and cache retention
admission
remote Event delivery
readiness and drain
```

The Runtime owns:

```text
Agent state consistency
Run and Turn sequencing
Model/Tool execution
canonical Events
Context propagation
local limits
execution settlement
```

A service handler, cache, or Routine executor MUST call these Runtime operations rather than implementing another state machine.
