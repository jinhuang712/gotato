# Agent Core

**Status:** Draft

> **The Agent is a Go goroutine with private state, a channel boundary, and one simple Loop.**

## 1. Boundary

Agent Core is Gotato's primary runtime. It supplies the execution unit that can be embedded in a Go process or reached through an Orchestrator:

```text
caller / Orchestrator
        │ command channel
        ▼
Agent handle ──► Agent goroutine
                    │
                    ├── private Agent state
                    ├── Model → Tool → Model Loop
                    ├── explicit capabilities
                    ├── cancellation
                    └── Event / result channels
```

The goroutine is not an optional implementation detail. The stable public handle hides the goroutine identity and exposes channel-backed operations.

Core does not import or require gRPC, Protobuf, Kubernetes, Gateway, cache, service admission, or provider SDKs. It does not decide how external Prompts are queued, prioritized, rejected, or routed.

## 2. Agent state

An Agent goroutine owns only its local state and capabilities:

```text
system instructions
Model contract
committed Messages
individual Tools and registered ToolSets
active ToolSets
Extensions
current Run state
runtime options and local limits
```

The state is accessed and mutated by the Agent goroutine. Callers receive snapshots or results and cannot mutate the state directly. An Agent does not own a Conversation registry, Host, Orchestrator, or shared application resource.

An Agent can process one Prompt or Continue at a time. When its current execution settles, it becomes available for another invocation. This is a single-flight execution property, not a global lock or a scheduling policy.

## 3. Channel-backed Core API

The public API may remain synchronous for ordinary Go callers while using channels internally:

```go
type Agent interface {
    Prompt(context.Context, Message) (RunResult, error)
    Continue(context.Context) (RunResult, error)
    Steer(Message) error
    FollowUp(Message) error
    Abort()
    WaitForIdle(context.Context) error
    Subscribe(EventHandler) (unsubscribe func())
    StateSnapshot() AgentSnapshot
    Reset() error
}
```

`Prompt` and `Continue` submit one execution command and wait on its result channel. They do not provide a queue for future external Prompts. If the Agent is already executing, the direct caller receives a typed not-available/busy result or must use its own scheduler.

`Steer` and `Abort` are control messages for the current Agent execution. `FollowUp` is a control message for a subsequent continuation; it is not a general user-request scheduler. The exact buffering and acceptance bounds are Core configuration, while policy for many external requests belongs to the caller or Host.

## 4. Canonical Loop

Each Agent goroutine runs one canonical Loop:

```text
receive one Prompt or Continue
create Run identity and Context
emit agent_start

repeat:
  admit next Turn
  build Model request from private state
  open Model stream
  assemble and commit assistant Message
  resolve and execute Tool Calls
  commit Tool Results
  emit turn_end
  process control messages at defined boundaries
  continue or settle

emit one agent_end
return RunResult through the result channel
```

The Loop does not inspect user connections, request queues, or platform state. It responds to commands delivered through its channel. Embedded and Hosted callers use the same Loop.

## 5. Model boundary

Core constructs Model requests, assembles streams, and commits transcript state. It consumes only a provider-neutral contract:

```go
type Model interface {
    Stream(context.Context, ModelRequest) (ModelStream, error)
}
```

A Model Router or provider adapter may select a provider, but it cannot mutate Agent state or create another Loop.

## 6. Tool boundary

Tools are explicit capabilities available to the Agent goroutine. Core owns:

```text
argument assembly
resolution and Schema validation
Pre-Tool-Use and Post-Tool-Use
bounded invocation
result commitment
Tool Events
```

The application or an adapter owns external authentication, protocol translation, and external resource policy.

## 7. Agent Routine

An Agent Routine is the running Go routine of an Agent:

```text
Agent identity
  + private state
  + one Agent goroutine
  + inbound command channel
  + outbound result/Event channels
```

A spawned Agent is another independent Agent Routine. It is not a child task object owned by the spawning Agent. The spawning operation may attach origin IDs or correlation metadata, but the two routines communicate through channels and do not share mutable state by default.

A `Run` is one Prompt or Continue processed by an Agent Routine. A Run is not a container that owns another Agent Routine.

## 8. Spawn and coordination

An Agent goroutine may create another Agent directly or send a spawn request through an explicit factory/capability:

```text
Agent A goroutine ── go / spawn ──► Agent B goroutine
        │
        └── optional factory / Orchestration channel ──► Agent B
```

Both paths create the same independent Agent Routine. The Orchestrator or application may decide admission, limits, routing, and whether to wait for B. Agent A does not acquire ownership of B. Cancellation of B is explicit; it is not implied merely by the fact that A created it.

## 9. Events

The Agent goroutine emits immutable canonical Events through its Event boundary. Local subscribers and Hosts consume those Events; neither is part of the Agent's private transcript state.

`agent_end` is the final canonical Event for a Run. The Agent does not wait for remote delivery before returning its result.

## 10. Cancellation and settlement

The Agent goroutine selects on the current Run Context and control channels. Cancellation stops new work and asks Model, Tools, Extensions, and any locally admitted work to settle. It does not roll back committed state.

Execution settlement is the point at which the Agent has completed the current Run and can accept the next one. Queue settlement and remote delivery are outside Core.

## 11. Limits

Core limits govern one Agent execution:

```text
Turns and Tool Calls
visible Tools and active ToolSets
parallel Tool workers
Tool result and progress volume
Run, Model, and Tool deadlines
```

The Host or caller governs external request queues, active streams, global quotas, and how many Agent goroutines to create.

## 12. Embedded use

```text
Go method / application goroutine
          │ channel-backed call
          ▼
       Agent goroutine
          │
          ├── Model
          └── Tools / Extensions
```

A method may send one Prompt and wait for its response. If the application accepts multiple user Prompts, the application—not Core—chooses whether to queue, reject, steer, or abort them.
