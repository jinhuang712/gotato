# Gotato Philosophy

**Status:** Draft
**Purpose:** Project constitution

> Keep Agent execution small and stable; compose hosting, transport, and infrastructure around it.

## 1. Mission

Gotato's primary product is a Go-native Agent Runtime Core for stateful, tool-using workloads. An Agent-as-a-Service host is an optional composition around that Core for applications that need remote access.

```text
Embedded: Application → Agent Core → Model / Tool
Hosted:   Client → Transport → Orchestrator → Agent Core
```

The Core is useful without a network. The Hosted mode is useful when callers need shared Agent capabilities, bounded concurrency, remote Events, and managed lifecycle.

Gotato does not claim a novel Agent algorithm, provider abstraction, or distributed actor model. It optimizes for a clear Go boundary, explicit ownership, bounded execution, predictable cancellation, and a Core that can be tested independently of hosting.

## 2. Boundary-first design

The Core boundary is stable from the first implementation. Public package publication may happen later, but the implementation must never depend on the host that currently calls it.

```text
Core contract
    ↓
embedded acceptance tests + hosted acceptance tests
    ↓
public compatibility commitment
```

Service use is one validation path; it is not a prerequisite for the Core to exist.

## 3. One canonical loop

Every execution path uses the same loop:

```text
Prompt / Continue
        ↓
Agent Core
  Model → Tool → Model
        ↓
Canonical Events + RunResult
```

An embedded caller, an Orchestrator, and a child Agent Routine may surround the loop differently, but none may reproduce it.

## 4. Core ownership

The Core owns:

```text
Agent state and transcript
Run and Turn sequencing
Model stream assembly
Tool resolution, validation, and commitment
ToolSet activation
Agent Routines
canonical Events
Context cancellation
local limits and terminal settlement
```

The Core does not own:

```text
gRPC or Protobuf
conversation routing across processes
Agent caches and service admission
Gateway or Kubernetes
business workflows
provider SDKs
```

## 5. Host ownership

The Orchestrator or Agent Host owns the coordination of multiple Core instances:

```text
Agent definitions and factories
conversation ownership
per-host and per-Agent admission
stream attachment
remote cancellation mapping
Event projection and bounded delivery
readiness, drain, and process lifecycle
```

These features are required only when the application chooses hosted mode. A normal Go service can use the Core directly and own its own business orchestration.

## 6. Infrastructure ownership

Gateway, load balancing, Kubernetes, storage, secrets, and deployment are infrastructure concerns. Gotato may provide integration contracts and examples, but infrastructure must not define Agent semantics.

```text
Infrastructure routes and hosts processes.
Orchestration owns hosted Agent coordination.
Core executes one Agent.
```

A Pod does not remove the need for in-process admission or Event delivery, but it does mean Gotato need not reimplement the surrounding platform.

## 7. Explicit concurrency

Concurrency exists at distinct boundaries:

```text
Infrastructure     routes traffic across Pods
Host               admits and schedules multiple Runs
Agent              serializes transcript mutation
Run                bounds Tools and Routines
Delivery           isolates slow consumers with bounded bridges
```

One Agent has at most one active mutating Run. Different conversations may run concurrently. Internal goroutines are owned, Context-aware, and joined at a defined settlement point.

## 8. Facts and delivery

The Core emits immutable canonical Events. Local consumers can observe them directly. A Host projects them across a remote boundary through a bounded bridge.

```text
Core Event → local observer
           → Host projection → bounded transport delivery
```

Execution settlement belongs to the Core. Delivery settlement belongs to the Host. A slow remote client must not become unbounded Core state.

## 9. Model boundary

The Core depends on a provider-neutral Model contract. A separate Model layer may provide routing, fallback, provider retries, rate limits, cost policy, and adapters.

```text
Core → Model contract → Model Router → Provider adapter
```

The router may choose where a Model request goes; it does not own the Agent transcript or Agent Loop.

## 10. Composition

Applications provide Agent definitions, Models, Tools, ToolSets, policies, and presentation. Gotato provides explicit constructors and contracts. Package-global discovery is not the composition model.

## 11. Review questions

1. Does this behavior belong to every Agent or only to a Host?
2. Can it execute without a network or process host?
3. Does it preserve one canonical loop?
4. Who owns its Context, bound, and settlement?
5. Does it introduce distributed state that needs an explicit routing or persistence contract?
6. Can embedded and hosted acceptance tests observe the same Core semantics?

## 12. Declaration

> Gotato is a stable Agent Core surrounded by optional orchestration, transport, and infrastructure. The Core is self-contained; the Host is concurrent; the deployment environment is replaceable; the Agent loop is singular.
