# 12. Delivery Roadmap

**Status:** Draft

> Stabilize Agent Core first, then add hosted orchestration and transport without changing Core semantics.

## Structural invariants

From the first code commit:

```text
Core has no transport, Host, Infrastructure, or provider SDK dependency
one canonical Agent Loop exists
Core exposes a self-contained in-process boundary
one Run emits exactly one terminal Event
all owned work has explicit Context, bound, and settlement
Host never duplicates the Core Loop
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
Agent Routines and Groups
```

**Exit:** Core supports staged capabilities, child Runs, bounded concurrency, and deterministic acceptance tests.

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
conversation ownership
cache/lease
lifecycle and drain
Event projection and bounded bridge
```

**Exit:** one process hosts multiple Core Agents and concurrent Runs without violating per-Agent exclusivity.

## Slice 6 — gRPC Hosted Service

```text
Protobuf contract
bidirectional Run stream
Start / Steer / FollowUp / Cancel
Go client
remote Event delivery and cancellation
```

**Exit:** a remote client executes the same Core Loop through the Host.

## Slice 7 — Multi-Pod Continuity

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
