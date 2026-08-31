# 00. Scope and Principles

**Status:** Draft

> **Agent as a Service.**
>
> **Core Native to Go.**

The specifications apply four principles:

1. **Agents are goroutines.**
2. **Agents own their work.**
3. **Infrastructure hosts. Orchestration coordinates. Agent Core executes.**
4. **Tight Core, Open Extensions.**

## 1. Deliverables

The repository defines a Go-native Agent runtime and an optional hosted composition:

```text
Agent Core
  Agent goroutine · private state · canonical Loop
  Model, Tool, ToolSet, Extension, and Agent Routine contracts
  canonical Events and local channel-backed subscriptions

Orchestration / Agent Host
  Agent creation · admission · request queue policy
  routing · coordination · remote Event delivery · lifecycle

Transport
  optional gRPC / Protobuf adapter and Go client

Platform integration
  guidance for existing Gateway and Kubernetes platforms
Model and capability adapters
```

A regular Go service may create and call Agent routines directly. A Hosted deployment may expose them remotely. Infrastructure hosts and routes processes but is not a Core dependency.

## 2. Core model

An Agent is a goroutine-backed stateful execution unit:

```text
Agent = private state + simple Loop + capabilities + channels
```

The Agent goroutine processes one Prompt or Continue at a time. It does not own external request queues, Conversation registries, Orchestrators, or shared application resources.

An Agent can request another independent Agent goroutine. Spawn provenance is correlation, not resource hierarchy.

## 3. Boundary rules

The Core MUST be self-contained and transport-independent. Core packages MUST NOT depend on:

```text
Protobuf or gRPC
Gateway or Kubernetes
Agent caches or service admission
application databases or process-hosting APIs
provider SDKs
```

Each Agent goroutine owns its state, capabilities, Loop, Events, cancellation, and local limits through Agent Core. The caller or Host owns external admission, queueing, priority, preemption, routing, and the number of Agent goroutines.

## 4. Required Core capability

Core MUST provide:

```text
stateful Agent goroutine with one active Prompt or Continue execution
Prompt and Continue
Model stream and Message assembly
Tool Call assembly and Schema validation
Pre-Tool-Use and Post-Tool-Use
sequential and bounded parallel Tools
ToolSet composition and staged activation
Steering and Follow-up control messages
Context cancellation and local limits
canonical Events and terminal settlement
focused Extensions
Agent Routine spawn and channel-backed results
Deterministic test fakes
```

Core MUST NOT provide a general external Prompt scheduler.

## 5. Optional Orchestration capability

The Host MUST provide these capabilities when Hosted mode is selected:

```text
named Agent definitions and factories
Agent routine creation and routing
per-Host admission and request queue policy
per-Agent dispatch when Free
priority, rejection, Steer, and Abort policy
transport stream attachment
canonical Event projection and bounded delivery
remote cancellation
readiness and graceful drain
```

The Host MUST coordinate through Agent channels. It MUST NOT mutate Agent state or reproduce the Core Loop.

## 6. Concurrency model

```text
Agent goroutine: one current Prompt/Continue execution
Orchestration goroutines: admission, queue, routing, coordination
Transport goroutines: wire receive/send and projection
Capability workers: bounded Tool or external work
```

These are communicating goroutines with distinct responsibilities. Each Agent owns its private state and work. Each coordinator owns its queues and channel endpoints.

## 7. One canonical Loop

The repository MUST contain exactly one Agent Loop. Every Agent goroutine uses it. Embedded callers, Hosted callers, and spawned Agents converge on the same Loop. No Host, scheduler, or capability may reproduce Model/Tool execution.

## 8. One terminal Event

A Run MUST emit exactly one terminal `agent_end` Event. Retry, context transformation, and control-driven continuation happen before it inside that Run. Nothing starts after it.

## 9. Channels and delivery

Core Events are immutable runtime facts delivered through local channel-backed observation. A Host may project them to remote channels or streams. Execution settlement and remote delivery settlement are separate concerns.

Protected Events cannot be silently dropped; optional progress may be coalesced under explicit bounds.

## 10. Model and capability boundaries

Core consumes a provider-neutral Model contract. Routing, provider selection, fallback, and provider SDKs belong to the Model layer. Tools, ToolSets, and Extensions enter through explicit contracts. No package-global discovery is part of Core composition.

## 11. Initial PoC

The initial Hosted PoC uses:

```text
one Host process
one Pod
local Agent goroutines and channels
process-local Agent registry and routing
```

Cross-Pod Conversation continuity is reserved for future work.

## 12. Presentation

Applications own business workflows and end-user CLI, TUI, Web, and chat interfaces. Gotato provides Agent runtime contracts, Hosted protocol contracts, Events, examples, and diagnostics.
