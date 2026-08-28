# 12. Delivery Roadmap

**Status:** Draft

> **Agent as a Service, native to Go.** Deliver Agent goroutines and their tight Core first, then add channel-connected orchestration and Hosted transport.

## Structural invariants

From the first code commit:

```text
Core has no transport, Host, Infrastructure, or provider SDK dependency
Agent is a goroutine-backed execution unit with private state and channels
one canonical Agent Loop exists
Core exposes a self-contained in-process boundary
initial PoC targets one Host process in one Pod
one Run emits exactly one terminal Event
all local work has explicit Context, bound, and settlement
Host never duplicates the Core Loop
Orchestration uses goroutines and channels rather than Agent resource ownership
```

Publishing a package can be staged; the boundary cannot.

## Slice 1 — Agent Core

```text
Agent state
Prompt / Continue
Model stream and Message assembly
canonical Events
Context cancellation
RunResult and terminal settlement
```

**Exit:** a Go program embeds Core, runs a scripted Model, receives Events, and cancels a Run.

## Slice 2 — Tool Loop

```text
Tool assembly and Schema validation
Model → Tool → Model
Tool Result commitment
Tool failure and panic handling
```

**Exit:** an embedded Agent executes a Tool exactly once and continues with its result.

## Slice 3 — Core composition

```text
Steering and Follow-up
ToolSets and activation
Extensions
bounded parallel Tools
Agent Routines and channel-backed spawn
```

**Exit:** Core supports staged capabilities, independent Agent Routines, bounded capability work, channel communication, and deterministic acceptance tests.

## Slice 4 — Model Layer

```text
Model Router contract
provider selection
provider adapters
bounded provider policy
```

**Exit:** Embedded Core can use a real provider through a provider-neutral Model contract without importing provider SDKs.

## Slice 5 — Orchestration / Agent Host

```text
Agent registry and factory
Host admission and concurrency
process-local Agent routing
handle cache
request queues and dispatch policy
lifecycle and drain
Event projection and bounded bridge
Agent Routine coordination
```

**Exit:** one process hosts multiple Agent goroutines and concurrent Runs while each Agent remains single-flight and the Host owns external queue policy.

## Slice 6 — gRPC Hosted Service

```text
Protobuf contract
bidirectional Run stream
Start / Steer / FollowUp / Cancel
Go client
remote Event delivery and cancellation
```

**Exit:** a remote client executes the same Core Loop through the Host.

## Reserved Slice 7 — Multi-Pod Continuity

This slice is deliberately outside the initial single-Pod PoC and remains a separately scoped future effort:

```text
keyed routing or distributed ownership
persistent state restoration
stream affinity and failure semantics
```

**Exit:** cross-Pod Conversation continuity has an explicit tested guarantee. Ordinary load balancing alone is not considered sufficient.

## Slice 8 — Platform Integration

```text
Gateway integration
Kubernetes deployment
health probes
observability
resource and autoscaling guidance
```

**Exit:** an existing platform can host the service without Core or Host depending on Kubernetes APIs.

## Ongoing

HTTP/Connect projections, capability adapters, remote Routines, durable Runs, governance, shared budgets, and richer Model routing proceed as independent packages backed by concrete use cases.
