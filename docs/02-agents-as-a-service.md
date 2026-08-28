# Agent as a Service

**Status:** Draft

> The Gotato product boundary is a first-party gRPC service backed by one transport-independent Agent runtime.

## 1. Role

Agent-as-a-Service makes stateful, tool-using Agents available to Go services and other gRPC clients through a standard contract.

```text
Client
  │ RunCommand
  ▼
Agent Service
  │
  ▼
Agent Runtime
  ├──► Model
  ├──► Tools and ToolSets
  ├──► Agent Routines
  └──► Canonical Events
             │
             ▼
       RunEvent stream
```

The service owns remote access, Agent resolution, admission, transport delivery, and process lifecycle. The runtime owns Agent execution semantics.

## 2. Service contract

```proto
service AgentService {
  rpc Run(stream RunCommand) returns (stream RunEvent);
}
```

The bidirectional stream represents an attached Run.

Client commands express:

```text
Start
Steer
FollowUp
Cancel
```

Server Events express:

```text
Agent and Turn lifecycle
Message streaming
Tool execution and progress
ToolSet activation
Agent Routine lifecycle
usage
terminal Run outcome
```

The Protobuf contract is the external compatibility surface. It projects runtime concepts without becoming the runtime domain model.

## 3. Attached Run lifecycle

```text
client opens stream
        ↓
Start identifies Agent, conversation, and Prompt
        ↓
service validates and admits
        ↓
service resolves an Agent
        ↓
canonical Run starts
        ↓
Events stream to client
        ↓
terminal Event settles
        ↓
command acceptance closes
```

`Start` is the first command. One stream attaches to one Run. Duplicate starts and commands after terminal settlement are protocol errors.

A Run emits exactly one terminal Event. A client that sees it needs no second completion signal, because retry, context compaction, and queued continuation all happen inside the Run.

## 4. Commands and execution

```text
Start     → Prompt or Continue
Steer     → accepted Steering queue
FollowUp  → accepted Follow-up queue
Cancel    → idempotent Run cancellation
```

Command acceptance preserves order. Runtime rules determine when accepted Steering and Follow-up Messages affect the next Turn.

The service translates commands into canonical Agent operations; it does not maintain a second Agent state machine.

## 5. Agent definitions and factories

Applications register named Agent definitions through factories:

```go
service, err := agentservice.New(
    agentservice.WithAgent("incident", incidentFactory),
    agentservice.WithAgent("repository", repositoryFactory),
)
```

Conceptually:

```text
Agent name + request context
             ↓
       AgentFactory
             ├── Model
             ├── Tools and ToolSets
             ├── Extensions
             └── limits and policies
             ↓
           Agent
```

Factories isolate application construction from service lifecycle. They can create ephemeral Agents or restore conversation-scoped state through an explicit state capability.

## 6. Conversation state

A service request may identify a conversation:

```text
Agent name + conversation key
              ↓
        Agent resolver
              ↓
     conversation-scoped Agent
```

The service coordinates per-key creation and ensures one active mutating Run per Agent. Concurrent conversations use separate Agent instances.

Conversation identity is a service concern. Messages and active ToolSets remain runtime state owned by the resolved Agent.

## 7. In-service Agent cache

A bounded in-process cache can retain conversation Agents:

```text
Conversation Key
       │
       ▼
┌──────────────────────────────┐
│ Agent Cache                  │
│ bounds · TTL · pin · eviction│
└──────────────┬───────────────┘
               ▼
         stateful Agent
```

Two properties of the thing being cached shape everything about it. A conversation Agent is a live Go object, so a restart loses it and no larger cache changes that: the cache is an optimization, and durable continuity comes from an explicit state provider instead. And an Agent can be mid-Run, so eviction is not free the way it is for a value cache — evicting a busy Agent would destroy state that an attached client is still watching.

That second property is why the cache needs a notion of an entry being in use, why expiry applies only to idle entries, and why two concurrent first requests for one key must produce one Agent rather than two. A conversation with two Agents is not a slow conversation; it is two conversations that both claim to be the same one.

## 8. Event delivery

Canonical Events cross the network through a bounded bridge:

```text
Runtime transition
      ↓
Canonical Event
      ↓
project and redact
      ↓
bounded Event bridge
      ↓
gRPC RunEvent
```

A Run can emit thousands of Events while a remote client accepts a few. The bridge is where that mismatch is resolved on purpose rather than by accident, so its capacity, its ordering guarantees, and its behavior when full are all stated.

What lets the bridge shed anything at all is that Events do not carry equal obligation. Lifecycle transitions and settled outcomes must reach the client in canonical order or the stream fails; streamed progress may be merged, thinned, or dropped, because progress never carries a fact its settling Event omits. A client that receives no progress at all still receives a complete history.

When the queue fills, the bridge slows the producer within a stated bound, merges pending progress, or fails the stream. What it does not do is silently drop a protected Event: a client whose stream fails knows it lost something, and a client missing a settled outcome does not. Blocking is bounded and Context-aware, because an unbounded wait would let one slow client hold a Run open indefinitely — the outcome the bridge exists to prevent.

Two different moments on this path are called settlement, and they have different owners:

```text
Execution settlement   the Run is over and owns no further work
Delivery settlement    the client has received everything it will receive
```

They are independent. A client that disconnects mid-stream ends delivery while execution continues to its own terminal Event, and a Run that finishes quickly may still have Events in flight. Conflating them produces a service that either cannot finish a Run because a socket is slow, or reports success before the client has anything.

## 9. Cancellation

```text
client Context
stream close
explicit Cancel
service drain deadline
      │
      ▼
Run Context
  ├──► Model stream
  ├──► Tool Uses
  ├──► awaited runtime observers
  └──► Agent Routines
```

Every cancellation source converges on the Run Context. Cancel is idempotent. The service waits for execution settlement, then completes or abandons delivery under its bounded policy.

## 10. Error boundary

The service distinguishes transport failures from Agent outcomes:

```text
invalid command       → protocol status
unknown Agent         → service status
admission rejected    → service status
Model terminal error  → terminal Run outcome and mapped status
Tool failure          → Tool Result when reasoning can continue
Routine failure       → Routine Result when parent can continue
```

Runtime error categories map to stable transport statuses and portable error details. Internal causes remain available to application-controlled diagnostics without leaking secrets.

## 11. Admission

Admission protects finite service resources:

```text
incoming Run
    ↓
service lifecycle check
    ↓
global and per-Agent bounds
    ↓
Agent availability
    ↓
accept or typed rejection
```

Admission is distinct from runtime limits. Service admission governs hosted capacity; runtime limits govern one accepted Run and its child work.

## 12. Readiness and drain

```text
Serving
  ├── liveness: process can operate
  └── readiness: new Runs may be admitted

Drain requested
      ↓
readiness becomes false
      ↓
new admission stops
      ↓
active Runs settle or reach drain deadline
      ↓
remaining work follows DrainPolicy
      ↓
telemetry and Event delivery settle
```

This lifecycle maps directly to Kubernetes probes and graceful termination.

## 13. Deployment forms

### Agent-enabled application service

```text
┌──────────────────────────────────────────────┐
│ Application Service                         │
│                                              │
│ Business API                                │
│ Agent gRPC API → Gotato → Domain ToolSets   │
└──────────────────────────────────────────────┘
```

### Dedicated Agent service

```text
┌──────────────────────────────────────────────┐
│ Dedicated Gotato Service                    │
│                                              │
│ Agent API                                   │
│   ├── Logs ToolSet    → Log API             │
│   ├── Metrics ToolSet → Metrics API         │
│   └── Repo ToolSet    → Repository API      │
└──────────────────────────────────────────────┘
```

Both forms preserve ownership of business data and APIs in their application domains.

## 14. Kubernetes topology

```text
Clients
   │ gRPC
   ▼
Kubernetes Service
   │
   ├──► Gotato Pod A ──► Model / capability APIs
   ├──► Gotato Pod B ──► Model / capability APIs
   └──► Gotato Pod C ──► Model / capability APIs
```

An attached Run remains on the Pod serving its stream. Replicas add admission capacity. Pod-local caches improve locality; durable state and routing remain explicit capabilities.

## 15. Direct Go use

The same runtime boundary can serve a direct in-process caller:

```text
Go application
      ↓
Runtime API
      ↓
Canonical Agent loop
```

Direct use removes transport mapping while preserving Agent, Run, Tool, Event, cancellation, and settlement semantics. The two paths differ in what surrounds the loop, never in the loop.

## 16. Ownership

```text
Runtime
  Agent state · Model/Tool loop · canonical Events · local limits

Service
  Agent factories · conversation ownership · admission · cache · drain

Transport
  Protobuf mapping · gRPC stream · network cancellation · client

Deployment
  Pods · Services · resources · secrets · routing · storage

Application
  Agent definitions · ToolSets · business meaning · presentation
```

The boundaries are directional: transport and service depend on runtime semantics; runtime execution remains complete without a network.
