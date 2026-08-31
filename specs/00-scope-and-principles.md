# 00. Scope and Principles

**Status:** Draft

> **Go-native Agent Runtime and Orchestration.**

> Gotato turns a self-contained Agent into an embeddable execution unit and, when needed, an addressable multi-Agent service.

Gotato has one product path: make one stateful Agent easy to embed as a normal Go interface, then make multiple Agents addressable and coordinatable through Orchestration without changing Core semantics. Hosted Agent Service is the service-facing form of that path. The specifications apply three principles:

1. **Agents are self-contained goroutines: each owns its state and work.**
2. **Infrastructure hosts. Orchestration coordinates. Host exposes. Agent Core executes.**
3. **Tight Core, Open Extensions.**

## 1. Deliverables

The repository defines an atomic Agent Core and the Orchestration path around it:

```text
Agent Core
  one Agent handle · private conversation state · canonical Loop
  Model and Tool contracts · cancellation · local limits
  canonical Events · result settlement

Orchestration
  Agent identity · handle retention · creation · routing
  admission · lifecycle · retirement · Conversation retention
  multi-Agent coordination · Event delivery

Host / Protocol Adapters
  remote command and Event mapping · readiness · drain

Adapters
  LLM adapters for Model providers
  Tool adapters for Go services and external systems
```

Core is sufficient for one directly held Agent. A system that manages multiple Agents MUST provide the Orchestration responsibilities above, either in application code or through a Gotato Orchestration package. A Host is the optional service-facing composition around Orchestration; it is required only when that Orchestration is exposed through a Hosted boundary.

Infrastructure is external. A Gotato deployment may use an existing Go process, HTTP/gRPC server, Gateway, Kubernetes cluster, storage, or secrets platform. Gotato provides integration contracts but does not implement those systems or count them as Gotato components.

## 2. Core model

An Agent is a callable, goroutine-backed stateful execution unit:

```text
Agent = private conversation state + simple Loop + capabilities + interface
```

The Agent processes one Prompt or Continue at a time. It owns its current work and local state. It does not own an external request queue, Conversation registry, Host, or shared application resource.

Core may keep the current conversation transcript required for multi-turn behavior. Long-term memory, retrieval, compaction, artifacts, and cross-session persistence are outside the minimal Core.

An Agent may create another independent Agent, but spawn provenance is correlation, not resource hierarchy. A single Agent can be called directly through its handle. Once multiple Agents must be found, revisited, scheduled, or coordinated, an external coordination owner is required; this may be application code or the Gotato Orchestration layer, with Host adding the remote service boundary. Orchestration is optional as a Gotato package, not optional as a responsibility.

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

Each Agent execution unit owns its state, capabilities, Loop, Events, cancellation, and local limits through Core. The application Orchestration or Host owns external identity, handle retention, admission, queueing, priority, preemption, routing, and the number of Agent instances. An AgentID identifies a Core Agent but cannot recover a lost in-memory handle.

LLM, Tool, and protocol adapters depend on Core contracts. Core does not depend on their concrete protocols.

## 4. Required Core capability

Core MUST provide:

```text
small callable Agent interface
stateful Agent execution with one active Prompt or Continue
explicit idempotent Agent close
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

## 5. Orchestration and Host capability

A system that manages multiple Agents MUST provide Orchestration with responsibilities equivalent to:

```text
named Agent definitions and factories
Conversation identity, handle retention, creation, and routing
admission and request queue policy
per-Agent dispatch when Idle
priority, rejection, Steer, and Abort policy
multi-Agent communication and result coordination
canonical Event observation and bounded delivery
Agent lifecycle, retirement, and cancellation
```

For one directly held Agent, these responsibilities may remain outside Gotato. For multiple Agents, they MUST exist in application code or in a Gotato Orchestration package. Orchestration MUST coordinate through the Agent contract; it MUST NOT mutate Agent state or reproduce the Core Loop. A retained Conversation MAY outlive the Agent handle that currently serves it; retirement and rehydration follow [spec 16](16-agent-lifecycle-and-retirement.md).

A Host is the optional service-facing composition around Orchestration. A Hosted system uses a protocol adapter to map wire commands and Events to Host; a direct Embedded application may use Orchestration without a Host.

## 6. Concurrency model

```text
Agent execution unit: one current Prompt/Continue execution
Orchestration goroutines: identity, admission, routing, coordination, lifecycle
Host / protocol goroutines: streams and delivery
Capability workers: bounded Tool or external work
```

These are communicating units with distinct responsibilities. Each Agent owns its private state and current work. Each coordinator owns its queues and interface endpoints.

## 7. Agent lifecycle

Run settlement does not close an Agent. A Core Agent MUST reject new Runs after it enters `Closing`, complete or explicitly cancel its current Run, close its local resources exactly once, and become `Closed`. Orchestration owns automatic retirement policies such as `AfterRun`, `AfterIdle`, and `Ephemeral`; a direct Core Agent defaults to `Retain`. See [spec 16](16-agent-lifecycle-and-retirement.md).

## 8. One canonical Loop

The implementation MUST contain exactly one Agent Loop. Every Agent uses it. Direct callers, application Orchestration, Hosted callers, and spawned Agents converge on the same Loop. No Orchestration, Host, scheduler, adapter, or capability may reproduce Model/Tool execution.

## 9. One terminal Event

A Run MUST emit exactly one terminal `agent_end` Event. Retry, context transformation, and control-driven continuation happen before it inside that Run. Nothing starts after it.

## 10. Events and delivery

Core Events are immutable runtime facts delivered through local observation. Orchestration MAY coordinate them, and a Host MAY project them to a protocol stream. Execution settlement and remote delivery settlement are separate concerns.

Protected Events cannot be silently dropped; optional progress MAY be coalesced under explicit bounds.

## 11. Initial PoC

The initial implementation SHOULD prove the progression without building infrastructure:

```text
Embedded, single: one existing Go service → Agent Core
Embedded, multi:  one existing Go service → Orchestration → Agent Core × N
Hosted:           one Host process → Orchestration → Agent Core × N
```

The Hosted PoC may reuse an existing HTTP/gRPC server and existing process environment. Cross-Pod continuity, durable Runs, long-term Memory, and platform-specific deployment are future integration work.

## 12. Presentation

Applications own business workflows, user interfaces, and request mapping. Gotato provides the Agent interface, Core semantics, Orchestration contracts, Host contracts, adapters, Events, examples, and diagnostics.
