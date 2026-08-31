# Agent Core

**Status:** Draft

> This document describes the small Go-native runtime behind every Gotato Agent.

## 1. The public idea

Agent Core turns a Model and optional Tools into a callable Agent:

```go
agent, err := gotato.NewAgent(
    gotato.WithModel(model),
    gotato.WithInstruction("You are a helpful assistant."),
    gotato.WithTools(tools...),
)
if err != nil {
    return err
}

result, err := agent.Prompt(ctx, gotato.UserMessage(input))
```

The public handle is the integration surface. It does not expose a Runner, require a Host, or require a service platform.

A minimal Core interface is:

```go
type Agent interface {
    Prompt(context.Context, Message) (RunResult, error)
}
```

Streaming and control are additive capabilities rather than prerequisites for the first call:

```go
type StreamingAgent interface {
    Agent
    Stream(context.Context, Message) (EventStream, error)
}

type ControllableAgent interface {
    Agent
    Continue(context.Context) (RunResult, error)
    Steer(Message) error
    FollowUp(Message) error
    Abort()
}
```

The exact exported names may evolve, but the public shape must remain small and ordinary for Go callers.

## 2. What Core owns

Core owns the semantics that make the Agent work:

```text
current conversation state
Model → Tool → Model Loop
Model and Tool contracts
Tool invocation and result commitment
Context cancellation and local limits
canonical Events and Run settlement
```

Core does not own external request queues, Conversation registries, service discovery, brokers, long-term memory, or deployment.

The current conversation state is the minimum state needed for a multi-turn Agent. It is not a separate Memory product.

## 3. Internal execution unit

Each Agent has one Go-native execution unit with private state:

```text
Agent handle
    ↓ private command boundary
Agent execution unit
    ├── conversation state
    ├── current Run
    ├── Model and Tools
    └── result / Event boundary
```

The implementation may use a goroutine and channels to confine state and serialize transitions. Callers use methods; they do not manage the goroutine or channel topology.

One Agent processes one Prompt or Continue at a time. If an external service needs a queue, it supplies that policy or uses the optional Host/Orchestration layer.

## 4. Canonical Loop

Core has one execution path:

```text
receive Prompt or Continue
create Run identity and Context
record the input in conversation state

repeat:
  build a Model request from private state
  open the provider-neutral Model stream
  assemble and commit one assistant Message
  resolve and execute Tool Calls
  commit Tool Results
  continue or settle

publish terminal result and Events
```

The loop is internal. It does not inspect user connections, service registries, request queues, or platform state. Embedded and Hosted callers reach the same Loop.

## 5. Model boundary

Core consumes a provider-neutral Model contract:

```go
type Model interface {
    Stream(context.Context, ModelRequest) (ModelStream, error)
}
```

An LLM Adapter implements this contract for a provider. It owns provider protocol, authentication, provider options, provider errors, and provider-level retry. Core decides when to call the Model and how the normalized stream affects Agent state.

A provider adapter cannot mutate Core transcript state, execute Tools, or create another Agent Loop.

## 6. Tool boundary

Tools are explicit capabilities:

```text
complete Tool arguments
  → resolve Tool
  → validate input
  → run at most once
  → finalize result
  → commit Tool Result
```

A Tool Adapter connects a Go service or external system to the Core Tool contract. The adapter owns external authentication, protocol mapping, and resource policy. Core owns invocation, cancellation, Events, and commitment.

Typed function helpers should make a small Go function easy to expose as a Tool. ToolSets and staged discovery are optional capabilities, not requirements for a basic Agent.

## 7. Conversation state

Core may keep:

```text
system instructions
committed Messages
registered Tools
active optional ToolSets
current Run state
local execution limits
```

This state is private to the Agent. Snapshots and results do not alias mutable Core state. Persistence, retrieval, compaction, and cross-session memory belong outside the minimal Core.

## 8. Control

Advanced callers may send:

```text
Continue  continue the current conversation
Steer    change direction at a safe boundary
FollowUp provide a subsequent continuation
Abort    cancel the current Run
```

These controls are handled by the same Loop. They do not turn Core into a general external request scheduler.

## 9. Events and settlement

Core emits canonical Events for committed transitions and declared operations. A Run has one terminal settlement. Core returns its result after local execution settles; it does not wait for remote delivery.

A local subscriber or Hosted Host may observe these Events. Remote projection is outside Core.

## 10. Limits and cancellation

Core bounds one Agent's local work:

```text
Turns and Tool Calls
parallel Tool workers
Tool result and progress volume
Run, Model, and Tool deadlines
```

Every blocking operation receives the Run Context. Cancellation prevents future work and asks active Model, Tool, and Extension work to settle. Committed state is not rolled back.

## 11. Embedded use

```text
Existing Go Service
        │ direct Agent interface
        ▼
    Agent Core
        ├── LLM Adapter
        └── Tool Adapters
```

No Host, protocol adapter, Registry, Broker, or new deployment platform is required. The existing service decides how to map its own request and response types.

## 12. Hosted use

```text
Client
  ↓ protocol adapter
Agent Host / Orchestration
  ↓ Agent interface
Agent Core
```

The Host adds remote access, admission, routing, lifecycle, and Event delivery. It uses the same Core Agent and does not reproduce the Loop.
