# Gotato Philosophy

**Status:** Draft  
**Purpose:** Project constitution

> Build a useful Go Agent service, run every Agent on one canonical loop, and preserve a small transport-independent kernel beneath the network boundary.

## 1. Mission

Gotato is a Go-native Agent-as-a-Service for intelligent, tool-using workloads.

```text
Client → Agent Service → Model → Tool → Model → Events → Client
```

It makes Agent execution remotely accessible, observable, cancellable, bounded, testable, and extensible. A compact Go runtime kernel implements the stateful Model/Tool loop inside the service.

Gotato's runtime semantics draw on Pi, a compact and extensible agent kernel written in TypeScript. The Go expression, the ToolSet model, the service boundary, and the delivery contracts are Gotato's own.

## 2. One canonical execution path

```text
Remote client
     │
     ▼
gRPC Agent Service
     │
     ▼
Canonical Agent Runtime
     │
     ├──► Model
     ├──► Tools and ToolSets
     ├──► Extensions
     └──► Agent Routines
```

Every hosted Agent uses one state model, one Model/Tool loop, one Event model, and one cancellation model.

A direct Go API can expose the same runtime boundary without introducing an embedded-only loop. One boundary serves both consumers, or it is the wrong boundary.

## 3. Product shape

Gotato provides a coherent service system:

```text
Protobuf Agent contract
first-party gRPC server and Go client
Agent factories and state ownership
canonical Event streaming
cancellation and bounded backpressure
readiness and graceful drain
Model, Tool, ToolSet, and Extension contracts
Agent Routine execution
```

Applications provide Agent definitions, Models, capabilities, policies, and presentation. Gotato provides no first-party CLI, TUI, web, or chat experience.

## 4. Design principles

### 4.1 Prove abstractions through service behavior

A runtime concept enters the architecture when a concrete Agent service behavior needs it and deterministic tests can describe it.

```text
caller need
    ↓
service behavior
    ↓
runtime contract
    ↓
acceptance test
```

This keeps the kernel grounded in executable use rather than speculative generality.

### 4.2 Keep transport outside the kernel

The service translates between wire contracts and runtime contracts:

```text
RunCommand
    ↓ map
Agent operation
    ↓
Canonical Event
    ↓ project
RunEvent
```

Transport concerns stay in service and adapter packages. Runtime Messages, Events, results, and errors remain ordinary Go domain types. The runtime does not import Protobuf, gRPC, Agent caches, Kubernetes APIs, or transport envelopes.

### 4.3 Keep the kernel small

The runtime kernel contains semantics required by Agent execution:

```text
Agent state
Messages
Model stream
Tool Calls and Results
Turns
Events
cancellation
local limits
terminal settlement
```

A small kernel keeps the complete control flow reviewable and testable without a network.

### 4.4 Let Models reason and the Runtime execute

The Model interprets input, selects visible Tools, combines results, and determines whether another Turn is useful.

The Runtime assembles streams, validates arguments, schedules Tools, propagates cancellation, enforces limits, commits transcript state, converts failures, and emits canonical Events.

### 4.5 Use one fact model across boundaries

Agent state transitions create canonical Events. Embedded observers, gRPC clients, logs, traces, and tests consume projections of the same facts.

```text
Runtime transition
      ↓
Canonical Event
      ├──► gRPC projection
      ├──► observer
      ├──► telemetry
      └──► test recorder
```

Transport projection can filter, redact, coalesce, or encode Events without changing their runtime meaning.

One Run produces exactly one terminal Event. Retry, context compaction, and queued continuation belong inside the Run rather than to an orchestration layer that could restart it afterwards. A caller that observes the terminal Event knows the Run is over and needs no second completion signal.

### 4.6 Prefer explicit Go composition

Models, Tools, ToolSets, Extensions, factories, and policies enter through constructors and explicit options. Their ownership and dependencies remain visible in code.

Reflection helpers and code generation may reduce boilerplate while preserving deterministic construction. Package-global registration and classpath-style discovery do not define the composition model.

### 4.7 Bound all owned work

Every Model stream, Tool batch, Event bridge, Agent Routine, cache, and service admission path has explicit ownership, cancellation, buffering, and settlement behavior.

```text
service Context
      ↓
Run Context
  ├── Model
  ├── Tools
  ├── subscribers
  └── Agent Routines
```

Execution never waits on a network peer. A remote consumer that cannot keep up is slowed, thinned, or disconnected by the service under an explicit bound. It does not hold the Model/Tool loop open, and it does not accumulate unbounded memory inside the Runtime.

### 4.8 Follow Go's strengths

```text
small interfaces
context.Context
ordinary typed errors
gofuncs and goroutines
bounded concurrency
explicit streams
constructors and functional options
```

## 5. Architectural layers

```text
┌──────────────────────────────────────────────────────────┐
│ Application                                              │
│ Agent definitions · ToolSets · business meaning · UI    │
├──────────────────────────────────────────────────────────┤
│ Transport                                                │
│ Protobuf · gRPC server/client · wire compatibility      │
├──────────────────────────────────────────────────────────┤
│ Agent Service                                            │
│ factory · state ownership · admission · bridge · drain  │
├──────────────────────────────────────────────────────────┤
│ Runtime Kernel                                           │
│ Agent · Run · Messages · Model/Tool loop · Events       │
├──────────────────────────────────────────────────────────┤
│ Adapters                                                 │
│ Model providers · capability protocols · observability   │
└──────────────────────────────────────────────────────────┘
```

Dependency direction points toward the runtime contracts. Deployment and transport packages can be replaced without redefining Agent execution.

## 6. Ownership vocabulary

```text
Runtime Kernel  runs canonical Agent semantics
Tool            performs one concrete operation
ToolSet         groups related operations for discovery
Extension       changes behavior at an explicit runtime joint
Agent Routine   manages a child Agent Run
Adapter         connects a contract to an external technology
Service         owns remote access and hosted Agent lifecycle
Transport       maps service behavior to a wire protocol
Application     owns business meaning and presentation
```

## 7. Scope

Gotato concentrates on Agent runtime and service infrastructure. External applications and specialized systems own:

```text
end-user presentation
business workflows and state machines
RAG and memory products
identity and approval products
arbitrary code distribution
cluster control planes
company-wide platform governance
```

Gotato integrates with these systems through Tools, ToolSets, Extensions, adapters, and service APIs.

## 8. Success

```text
a Go service can call an Agent through the official gRPC client
a hosted Run streams ordered, correlated, terminal Events
a caller can cancel the complete Run ownership tree
an Agent can use bounded Tools, ToolSets, and Agent Routines
the runtime loop can be tested without gRPC or provider networks
the service does not duplicate or redefine runtime semantics
a slow or disconnected client is bounded without stalling the Runtime
a direct Go consumer can reuse the same runtime boundary
```

## 9. Review questions

1. Which observed service behavior requires this concept?
2. Which layer owns its state and failure semantics?
3. Does it preserve one canonical Agent loop?
4. Can the runtime behavior be tested without transport?
5. Does a wire type leak into the runtime kernel?
6. Is concurrency, buffering, cancellation, and settlement bounded?
7. Is the abstraction stable enough for more than one consumer?

## 10. Declaration

> Gotato exposes Agents as a service and structures its code around a transport-independent runtime kernel. The service is first-class, the loop is singular, the boundaries are explicit, and every owned operation is bounded.
