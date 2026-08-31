# 00. Scope and Principles

**Status:** Draft

> **Agent as a Service.**

> Gotato is a minimal, Go-native runtime for self-contained, stateful Agents.

Gotato has one product goal: make a stateful Agent as easy to add to an existing Go service as a normal Go interface, while keeping a direct path to Hosted Agent Service. The specifications apply three principles:

1. **Agents are self-contained goroutines: each owns its state and work.**
2. **Infrastructure hosts. Orchestration coordinates. Agent Core executes.**
3. **Tight Core, Open Extensions.**

## 1. Deliverables

The repository defines a minimal Agent Core and an optional Hosted composition:

```text
Agent Core
  small Agent interface · private conversation state · canonical Loop
  Model and Tool contracts · cancellation · local limits
  canonical Events · result settlement

Agent Host / Orchestration
  Agent creation · routing · admission · lifecycle
  optional request policy · Event delivery · protocol attachment

Adapters
  LLM adapters for Model providers
  Tool adapters for Go services and external systems
  optional protocol adapters for Hosted access
```

Infrastructure is external. A Gotato deployment may use an existing Go process, HTTP/gRPC server, Gateway, Kubernetes cluster, storage, or secrets platform. Gotato provides integration contracts but does not implement those systems or count them as Gotato components.

## 2. Core model

An Agent is a callable, goroutine-backed stateful execution unit:

```text
Agent = private conversation state + simple Loop + capabilities + interface
```

The Agent processes one Prompt or Continue at a time. It owns its current work and local state. It does not own an external request queue, Conversation registry, Host, or shared application resource.

Core may keep the current conversation transcript required for multi-turn behavior. Long-term memory, retrieval, compaction, artifacts, and cross-session persistence are outside the minimal Core.

An Agent may create another independent Agent, but spawn provenance is correlation, not resource hierarchy. Multi-Agent coordination is an optional Host or application capability.

## 3. Boundary rules

Core MUST be self-contained and protocol-independent. Core packages MUST NOT depend on:

```text
Protobuf or gRPC
Gateway or Kubernetes
service registries or message brokers
application databases or process-hosting APIs
provider SDKs
long-term Memory or Artifact products
```

Each Agent execution unit owns its state, capabilities, Loop, Events, cancellation, and local limits through Core. The caller or Host owns external admission, queueing, priority, preemption, routing, and the number of Agent instances.

LLM, Tool, and protocol adapters depend on Core contracts. Core does not depend on their concrete protocols.

## 4. Required Core capability

Core MUST provide:

```text
small callable Agent interface
stateful Agent execution with one active Prompt or Continue
Model stream and Message assembly
Tool Call assembly and Schema validation
Tool execution and Tool Result commitment
Context cancellation and local limits
canonical Events and terminal settlement
```

Core SHOULD provide the following as additive capabilities without complicating the basic path:

```text
Continue, Steer, Follow-up, and Abort
sequential and bounded parallel Tools
ToolSet composition and staged activation
focused Extensions
Agent Routine spawn and channel-backed coordination
```

Core MUST NOT require a general external Prompt scheduler, a service registry, a broker, a workflow engine, or a Memory product.

## 5. Optional Host / Orchestration capability

A Hosted composition MAY provide:

```text
named Agent definitions and factories
Agent creation and routing
per-Host admission and request queue policy
per-Agent dispatch when Free
priority, rejection, Steer, and Abort policy
protocol stream attachment
canonical Event projection and bounded delivery
remote cancellation
readiness and graceful drain
```

The Host MUST coordinate through the Agent contract. It MUST NOT mutate Agent state or reproduce the Core Loop.

A protocol adapter maps wire commands and Events to the Host interface. It is optional for Embedded use and is not a Core dependency.

## 6. Concurrency model

```text
Agent execution unit: one current Prompt/Continue execution
Host goroutines: admission, routing, coordination, lifecycle, delivery
Capability workers: bounded Tool or external work
```

These are communicating units with distinct responsibilities. Each Agent owns its private state and current work. Each coordinator owns its queues and interface endpoints.

## 7. One canonical Loop

The implementation MUST contain exactly one Agent Loop. Every Agent uses it. Embedded callers, Hosted callers, and optional spawned Agents converge on the same Loop. No Host, scheduler, adapter, or capability may reproduce Model/Tool execution.

## 8. One terminal Event

A Run MUST emit exactly one terminal `agent_end` Event. Retry, context transformation, and control-driven continuation happen before it inside that Run. Nothing starts after it.

## 9. Events and delivery

Core Events are immutable runtime facts delivered through local observation. A Host MAY project them to a protocol stream. Execution settlement and remote delivery settlement are separate concerns.

Protected Events cannot be silently dropped; optional progress MAY be coalesced under explicit bounds.

## 10. Initial PoC

The initial implementation SHOULD prove both forms without building infrastructure:

```text
Embedded: one existing Go service → Agent Core
Hosted:   one Host process → local routing → Agent Core
```

The Hosted PoC may reuse an existing HTTP/gRPC server and existing process environment. Cross-Pod continuity, durable Runs, long-term Memory, and platform-specific deployment are future integration work.

## 11. Presentation

Applications own business workflows, user interfaces, and request mapping. Gotato provides the Agent interface, Core semantics, optional Host contracts, adapters, Events, examples, and diagnostics.
