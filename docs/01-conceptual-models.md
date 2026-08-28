# Conceptual Models

**Status:** Draft

> Separate the Agent goroutine that executes, the channels that connect it, and the Orchestration goroutines that decide what happens next.

## 1. Layered system

```text
Application / Client
        │ commands and results
        ▼
Transport goroutines (optional)
        │ channels
        ▼
Orchestration goroutines
  admission · queue · routing · delivery
        │ channels
        ▼
Agent goroutine
  private state · simple Loop · capabilities
        │
        ├── Model contract → provider adapter
        └── Tool / Extension → capability adapter
```

Infrastructure may host the process and carry the network:

```text
Client → Gateway / LB → Pod → Transport → Orchestration → Agent goroutine
```

Infrastructure placement is not Agent semantics.

## 2. Agent

An Agent is a Go-native, stateful execution unit:

```text
Agent = identity + private state + goroutine + channels
```

The Agent goroutine owns only its own state and capabilities. It runs one simple canonical Loop and does not own a Conversation registry, request queue, Host, Orchestrator, or shared application resource.

The public Agent handle may hide the goroutine and expose synchronous convenience methods. Internally those methods send commands and await response channels.

## 3. Agent Routine

An Agent Routine is the running form of an Agent:

```text
Agent Routine = Agent goroutine + channel endpoints
```

A Routine is not a wrapper around a child Agent Run, and a goroutine is not merely an implementation detail. A spawned Agent creates another independent routine. Spawn provenance can be recorded, but there is no resource ownership hierarchy between routines.

## 4. Agent state and availability

The Agent owns:

```text
system instructions
Model contract
committed Messages
Tools and ToolSets
Extensions
current Run state
local execution limits
```

The Agent can be `Free` or `Busy` with respect to its current execution. It handles one Prompt or Continue at a time. `Free` means the routine can accept another execution command; it does not mean the Agent owns a user session or external resource.

## 5. Run and Turn

A Run is one Prompt or Continue processed by an Agent Routine. It has an identity, Context, Events, and settled Result.

A Turn is one Model request and the Tool batch produced by that response:

```text
turn_start → Model response → Tool batch → Tool Results → turn_end
```

A Run is not a parent container for another Agent. A spawned Agent has its own routine and its own Run.

## 6. The canonical Loop

Every Agent Routine uses one Loop:

```text
Prompt / Continue
       ↓
Model → Tool → Model → ...
       ↓
canonical Events + RunResult
```

The Loop owns Agent state transitions and capability execution. It does not inspect external user behavior or choose a request queue policy.

## 7. Orchestration

Orchestration is a collection of Go goroutines connected by channels. It coordinates Agent routines without becoming their state owner:

```text
incoming requests
        ↓
request goroutine / scheduler
        ├── reject while Busy
        ├── FIFO or priority queue
        ├── dispatch when Free
        ├── Steer at a boundary
        └── Abort immediately
        ↓
Agent channel
```

Orchestration may create Agent routines, coordinate groups, attach transport streams, forward Events, and manage lifecycle. It does not edit Agent state or reproduce the Agent Loop.

## 8. Conversation

A Conversation is an application or Host routing key. In the single-Pod PoC, a process-local registry may map it to an Agent handle:

```text
agent name + conversation key
              ↓
process-local routing table
              ↓
Agent channel
```

This is routing, not Agent ownership. The Agent does not own the registry, and ordinary multi-Pod continuity remains a future concern.

## 9. Transport

Transport maps external messages to channel-backed Orchestration or Agent commands:

```text
RunCommand → transport goroutine → Host command channel
Agent Event → Event channel → projection → RunEvent
```

Protobuf and gRPC types stop at the transport boundary.

## 10. Model and capabilities

The Core consumes a provider-neutral Model contract. Tools, ToolSets, and Extensions are explicit capabilities installed on an Agent:

```text
Agent goroutine
   ├── Model contract → Router → Provider
   ├── Tool / ToolSet → capability adapter
   └── Extension hooks
```

Adapters own external protocol, authentication, and provider policy. The Agent owns capability invocation and commitment into its local state.

## 11. Embedded and Hosted

### Embedded

```text
Go method / application goroutine
        │ channel-backed call
        ▼
Agent goroutine
```

The application is free to provide its own request queue or simply call one Prompt and wait for the result.

### Hosted

```text
Client → Transport goroutines → Orchestration goroutines → Agent goroutine
```

Hosted mode adds remote access and scheduling policy. It does not add another Agent implementation.

## 12. Boundaries

| Concern | Primary owner |
|---|---|
| Agent state and canonical Loop | Agent goroutine / Core |
| Agent capabilities | Core contracts and adapters |
| Prompt admission and queue policy | Application or Orchestration |
| Agent creation and routing | Orchestration |
| Wire mapping | Transport |
| Deployment and network | Infrastructure |
| Provider selection | Model layer |

The system is connected by channels and explicit contracts rather than a hierarchy of resource owners.
