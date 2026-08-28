# 01. Core Domain

**Status:** Draft

> Agent owns state; Run owns one invocation; Turn owns one Model response and its Tool batch.

## 1. Identity types

The implementation SHOULD use distinct Go types:

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
```

A Core Run ID is non-empty, immutable, and unique for process lifetime. Turn numbers start at 1 and are never reused within a Run. Message and Tool Call identities are stable in their scopes. Host request, stream, trace, and provider IDs are additive and must not replace Core identities.

## 2. Agent state

An Agent contains:

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
```

The exact fields may evolve, but the values and ownership must remain. Snapshots copy nested data and cannot mutate Core state.

State transitions:

```text
Idle ──Prompt/Continue──► Running
Idle ──Reset─────────────► Idle
Running ──terminal──────► Idle
Running ──Prompt/Continue► busy error
Running ──Reset──────────► invalid-state error
```

Steering and Follow-up are bounded queue operations and do not create a second Run.

## 3. Run

```go
type RunResult struct {
    RunID        RunID
    Status       RunStatus
    FinalMessage *Message
    Usage        Usage
    Error        *RuntimeError
}
```

A Run is one accepted Prompt or Continue. Its lifecycle is:

```text
Created → Accepted → Running → Settling → Settled
```

A terminal Run stores one immutable result, produces one `agent_end`, starts no new Model/Tool/Routine/retry/queue work, and returns the same result from repeated waits. Core never waits for remote delivery.

## 4. Turn

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

A Turn ends only after the assistant Message, every Tool outcome, and Tool Result Messages in assistant source order are committed.

## 5. Tool Use

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

Core creates a ToolUse only after complete argument assembly, resolution, and Schema validation. One executor is invoked at most once for one ToolUse. An explicit retry creates a new identified use.

## 6. Snapshot

A snapshot MUST preserve transcript order, active ToolSet order, queue counts, busy state, and active Run identity while omitting mutable executors, Contexts, provider clients, and locks. All slices, maps, byte arrays, and nested content are copied or immutable.

## 7. Invariants

Core MUST preserve:

```text
Prompt Message committed before first Model request
assistant Message committed before Pre-Tool-Use
Tool Result references a Tool Call in the current assistant Message
batch Tool Results committed in source order
ToolSet activation committed between Turns
queue acceptance order preserved
Reset and Run mutation mutually exclusive
terminal Run starts no work
snapshot cannot alias mutable Agent state
```

## 8. Core and Host relationship

Conversation keys, factories, caches, admission, and cross-process routing belong to Host. The Core receives an already-created Agent and owns only its local state and execution semantics.
