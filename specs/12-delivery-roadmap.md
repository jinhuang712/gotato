# 12. Delivery Roadmap

**Status:** Draft

> Deliver the atomic Agent Core first, then make multiple Core Agents addressable through Orchestration and expose that system as a Service.

The roadmap follows one product path:

```text
Model + Tools → Agent Core → Orchestration → Hosted Agent Service
```

Infrastructure is never a Gotato implementation slice. Every stage must run in an existing Go process and use the platform already available to the application.

## Structural invariants

From the first code commit:

```text
Core has no protocol, infrastructure, registry, broker, or provider SDK dependency
Agent is a Go-native execution unit with private state
one canonical Agent Loop exists
Agent exposes a small in-process interface and explicit close
Run settlement is distinct from Agent closure
current conversation state is local and bounded
one Run has one terminal result and terminal Event
all local work has explicit Context, bound, and settlement
Orchestration and Host never duplicate the Core Loop
LLM and Tool integrations enter through adapters
```

The implementation may grow around Core, but the first-call path must remain small.

## Slice 1 — Minimal Agent Core

```text
Agent interface
Model contract
conversation state
Prompt and RunResult
canonical Model → Tool → Model Loop
Context cancellation
basic limits and terminal settlement
```

**Exit:** an ordinary Go program constructs an Agent with a scripted Model, calls one Prompt, receives a result, and closes the Agent without a Runner, Host, SessionService, or platform dependency.

## Slice 2 — Tools and LLM adapters

```text
provider-neutral Model values
one official LLM adapter
typed Go function Tool helper
Tool argument assembly and Schema validation
Tool result commitment
Tool failure and panic handling
```

**Exit:** an existing Go service can add one Model and one Go Tool and complete a useful Agent request with a small amount of code.

## Slice 3 — Streaming and Core observation

```text
streaming response / Event boundary
canonical Event order
local subscribers
Run, Model, and Tool deadlines
Abort and bounded local work
```

**Exit:** the service can stream progress, cancel a Run through Context, and observe a bounded canonical Event sequence.

## Slice 4 — Core composition

```text
Continue
Steer and Follow-up
focused Extensions
optional ToolSets and staged activation
bounded parallel Tools
```

**Exit:** advanced callers can extend the same Loop without changing the basic Agent interface or forking execution behavior.

## Slice 5 — Embedded Orchestration

```text
Agent identity and handle retention
Conversation record and key-based routing
Agent factory and concurrent get-or-create
per-Conversation Agent lifecycle and retirement policy
admission and request policy
per-Agent dispatch and multi-Agent coordination
bounded Event observation and result aggregation
```

**Exit:** an existing HTTP, gRPC, or Go service can host multiple Agent instances, revisit them by Conversation key, retire idle or ephemeral Agents, and rehydrate retained Conversations while Core remains single-flight per Agent. The application may implement this layer itself or adopt the Gotato Orchestration package; the responsibility is not optional for multi-Agent use.

## Slice 6 — Hosted Agent Service

```text
Host semantic interface
one protocol adapter
remote Conversation and Agent selection
Agent close / retirement mapping
Steer / Follow-up / Cancel mapping
readiness and graceful drain
```

**Exit:** a remote caller can address and coordinate the same multiple Core Agents through Host and Orchestration without a second Agent implementation or Loop.

## Slice 7 — Orchestration extensions

```text
bounded Agent handle cache and idle retirement
Agent Routine coordination and ephemeral children
priority and preemption policies
Conversation persistence and rehydration
Event projection and delivery bridges
optional cross-process adapters
```

**Exit:** coordination policies can evolve around stable Core semantics without moving identity, routing, or scheduling into Core.

The first adapter may be gRPC, HTTP, SSE, or an existing service protocol. The adapter is not a Core dependency. Lifecycle and retirement contracts are specified in [Agent lifecycle and retirement](16-agent-lifecycle-and-retirement.md).

## Reserved — Distributed placement

The following remain separate future contracts:

```text
cross-process Agent placement
multi-Pod Conversation continuity
durable Runs and restoration
remote Agent Routines
reconnection and resumable delivery
```

They require explicit ownership, idempotency, failure, and compatibility guarantees. Ordinary load balancing is not a continuity guarantee.

## Ongoing integration work

Platform-specific work provides examples and compatibility tests rather than Gotato infrastructure:

```text
existing Gateway or Kubernetes deployment
health, readiness, and drain wiring
observability adapters
provider conformance
race-tested Core and Orchestration/Host fixtures
```

HTTP/Connect projections, additional LLM and Tool adapters, and richer Orchestration policies proceed as packages backed by concrete use cases.
