# 01. Core Domain

**Status:** Draft

> **Agents are self-contained goroutines: each owns its state and work.**

## 1. Identity types

The implementation SHOULD use distinct Go types:

```go
type AgentID string
type AgentName string
type ConversationID string
type ConversationKey string
type RunID string
type TurnNumber uint32
type MessageID string
type ToolCallID string
type ToolSetName string
type ToolName string
type SpawnID string
type AgentGeneration uint64
```

An `AgentID` identifies one Agent goroutine and its private state. A `RunID` identifies one Prompt or Continue handled by that routine. A `SpawnID` correlates an Agent creation request without implying ownership or a parent/child resource hierarchy.

Host request, stream, trace, and provider IDs are additive and must not replace Core identities.

## 2. Agent runtime

An Agent is a callable, goroutine-backed execution unit:

```text
Agent
  ├── private AgentState
  ├── inbound command channel
  ├── result channel(s)
  └── Event channel
```

Agent state is never directly mutated by callers, Hosts, or other Agents. Commands are delivered to the Agent goroutine, which is the only authority allowed to mutate that Agent's state.

Run settlement does not end the Agent. The Core lifecycle is:

```text
Created → Idle ⇄ Busy → Closing → Closed
```

A directly held Agent remains usable after a Run settles until its owner explicitly closes it. An Agent entering `Closing` accepts no new Prompt or Continue. Close behavior, retirement intent, and Conversation retention are defined in [spec 16](16-agent-lifecycle-and-retirement.md).

```go
type AgentState struct {
    SystemInstructions string
    Messages           []Message
    RegisteredTools    []ToolState
    RegisteredToolSets []ToolSetState
    ActiveToolSets     []ToolSetName
    Extensions         []ExtensionState
    Run                *ActiveRun
    Options            RuntimeOptions
}
```

The exact fields may evolve, but private state, Agent-owned work, and execution confinement remain. Snapshots copy nested data and cannot mutate Core state.

## 3. Availability and single-flight execution

An Agent goroutine processes one Prompt or Continue at a time:

```text
Idle ── accept one command ──► Busy
Idle ◄──── terminal result ─── Busy
```

This is a local execution property. Core does not own the external request queue. A direct caller may receive a typed busy/not-available result when the Agent is Busy. Application Orchestration may instead queue, reject, prioritize, steer, or abort before dispatching a command.

The Agent does not own a Conversation registry, Host, Orchestration, or shared application resource.

## 4. Core operations

`Prompt` and `Continue` submit one execution command. `Steer`, `FollowUp`, and `Abort` submit control messages for the current or next execution. The exact external request policy is not part of the Core:

```text
application Orchestration / Host policy
  reject · queue · priority · preempt
          ↓ channel
Agent command
```

Steering and follow-up data may be bounded inside the Agent's command protocol, but that buffer is not a general user-request scheduler.

## 5. State transitions

```text
Idle ──Prompt/Continue──► Busy
Idle ──Reset─────────────► Idle
Busy ──terminal──────────► Idle
Idle/Busy ──close────────► Closing → Closed
Busy ──new Prompt────────► not accepted by Agent
```

`Reset` cannot mutate an Agent while its current execution is active. A Host may choose to wait for `Idle` before sending Reset.

## 6. Run

```go
type RunMetrics struct {
    ElapsedMS      int64
    Turns          uint32
    ToolCalls      uint32
    TextBytes      uint64
    ReasoningBytes uint64
}

type RunResult struct {
    RunID        RunID
    Status       RunStatus
    FinalMessage *Message
    Usage        Usage
    Metrics      RunMetrics
    Error        *RuntimeError
}
```

A Run is one accepted Prompt or Continue. Its lifecycle is:

```text
Created → Accepted → Running → Settling → Settled
```

A terminal Run stores one immutable result, produces one `agent_end`, starts no new Model/Tool/retry work, and returns the same result from repeated waits. Core does not wait for remote delivery. `agent_end` terminates the Run; it does not close the Agent.

A Run does not own another Agent. An Agent created by a spawn request has its own AgentID, goroutine, state, channels, and Run identities.

## 7. Turn

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

## 8. Tool Use

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

## 9. Snapshot

A snapshot MUST preserve transcript order, active ToolSet order, current execution status, and active Run identity while omitting mutable executors, Contexts, provider clients, channels, and locks. All slices, maps, byte arrays, and nested content are copied or immutable. A retirement snapshot is taken only at a quiescent boundary after the current Run settles; resuming an active Run after crash is a separate checkpoint contract and is not implied by Conversation rehydration.

## 10. Invariants

Core MUST preserve:

```text
Prompt Message committed before its first Model request
assistant Message committed before Pre-Tool-Use
Tool Result references a Tool Call in the current assistant Message
batch Tool Results committed in source order
ToolSet activation committed between Turns
one Agent goroutine mutates one Agent's private state
terminal Run starts no work
snapshot cannot alias mutable Agent state
```

## 11. Agent communication

Agent-to-Agent and Agent-to-Orchestration communication uses explicit channels or channel-backed handles. No caller directly reaches another Agent's private state. Spawn provenance is correlation metadata, not ownership.

Conversation records, factories, request queues, caches, admission, retirement, and cross-process routing belong to Orchestration or the caller. The Core receives commands for one Agent and executes them locally. A multi-Agent caller must retain handles or provide an external key-to-handle mapping; an AgentID alone cannot recover a lost in-memory Agent. Cross-process or restart recovery is outside the current Core contract unless the lifecycle and persistence contracts in [spec 16](16-agent-lifecycle-and-retirement.md) are implemented.
