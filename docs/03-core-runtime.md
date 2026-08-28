# Agent Core

**Status:** Draft

> Agent Core is Gotato's primary deliverable: a self-contained Go runtime that executes one canonical Agent loop. It can be embedded in an existing service or hosted by an Orchestrator.

## 1. Boundary

```text
Application or Orchestrator
          │ Core API
          ▼
     Agent Core
          │
          ├── Model contract
          ├── Tool / ToolSet contracts
          ├── Agent Routines
          └── Canonical Events
```

Core does not import or require gRPC, Protobuf, Kubernetes, Gateway, cache, service admission, or provider SDKs. This boundary is the project's main engineering commitment, not a claim that the underlying Agent loop is novel.

## 2. Core state

An Agent owns:

```text
system instructions
Model contract
committed Messages
individual Tools and registered ToolSets
active ToolSets
Extensions
Steering and Follow-up queues
runtime options and limits
active Run state
```

One Agent has one serialized mutation owner and at most one active mutating Run. This can be implemented with an owner goroutine/actor, but the public contract promises state ownership rather than a particular scheduling layout.

## 3. Core API

The Core exposes operations equivalent to:

```go
Prompt(context.Context, Message) (RunResult, error)
Continue(context.Context) (RunResult, error)
Steer(Message) error
FollowUp(Message) error
Abort()
WaitForIdle(context.Context) error
Subscribe(EventHandler) (unsubscribe func())
StateSnapshot() AgentSnapshot
Reset() error
```

Channels, locks, provider objects, and transport envelopes stay private. `Prompt` and `Continue` wait for execution settlement, not remote delivery.

## 4. Canonical loop

```text
accept Prompt or Continue
create Run Context and Run ID
emit agent_start

repeat:
  admit next Turn
  transform and convert context
  resolve visible Tools
  open Model stream
  assemble one assistant Message
  commit assistant Message
  preflight and execute Tool batch
  commit Tool Results in source order
  emit turn_end
  apply TurnStopper and queued Steering/Follow-up
until terminal decision

emit one agent_end
await terminal observers
return RunResult
```

The loop is implemented once. Embedded callers, Hosts, and child Routines all enter through this boundary.

## 5. Model boundary

Core owns Model request construction, stream assembly, and transcript commitment. It depends only on a provider-neutral contract:

```go
type Model interface {
    Stream(context.Context, ModelRequest) (ModelStream, error)
}
```

A Model Router or provider adapter may select a provider, but it cannot mutate Core state or replace the loop.

## 6. Tool boundary

A Tool is one operation. A ToolSet is a named capability domain. Core owns:

```text
argument assembly
resolution
Schema validation
Pre-Tool-Use and Post-Tool-Use
at-most-once executor invocation
bounded parallel execution
result commitment
```

Applications may implement Tools for databases, Redis, HTTP, MCP, workflows, sandboxes, or remote Agents.

## 7. Agent Routines

A Routine is a managed child Agent Run using the same Core loop:

```text
parent Run → Routine → distinct child Agent → child Run
```

Parent Context cancellation reaches child work. Routine count, depth, concurrency, progress, and deadlines are bounded. Local execution may use goroutines; remote Routine placement belongs to a Host or adapter.

## 8. Events

Core Events are immutable facts with per-Run sequence and correlation:

```text
agent_start
turn/message lifecycle
Tool lifecycle
ToolSet activation
Routine lifecycle
turn_end
agent_end
```

`agent_end` is the final canonical Event and the only terminal signal. Local subscribers are awaited, in-process consumers. Remote delivery is not a Core subscriber; a Host projects Events through a bounded bridge.

## 9. Cancellation and settlement

All owned operations derive from the Run Context:

```text
Run Context
  ├── Model stream
  ├── Tool Uses
  ├── Extensions and observers
  └── Agent Routines
```

Cancellation prevents new work and asks active work to settle; it does not roll back committed state. Execution settlement occurs when Core-owned work and terminal observers have settled.

## 10. Limits

Core limits include:

```text
Turns and Tool Calls
visible Tools and active ToolSets
parallel Tool Uses
Tool result and progress volume
Routine count, depth, and concurrency
Run, Model, Tool, and Routine deadlines
```

Host admission, service quotas, billing, and process resources are not Core limits.

## 11. Errors

Tool failures normally become failed Tool Results so the Model may continue. Protocol failures, blocking Extension failures, fatal Model failures, cancellation, and exhausted Core limits settle the Run. Errors are typed and safe for callers; private causes remain diagnostics.

## 12. Embedded use

```text
existing Go service
       ▼
   Agent Core
       ▼
Model Router / Provider
```

The application owns request routing, business state, and any higher-level workflow. It can use the Core for a single analysis, a stateful Agent, or explicit Tool-driven data inspection without deploying a separate Agent Service.
