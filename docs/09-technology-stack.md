# Technology Stack and Deployment Boundaries

**Status:** Draft

> Build the Agent Core first with Go primitives; add protocol adapters for remote access and use the existing platform for deployment.

## 1. Stack map

The Core is the primary implementation target. The following diagram describes the optional hosted path, not a requirement that every deployment include all layers:

```text
Existing platform
  Gateway · LB · Kubernetes · storage · secrets
          │
          ▼
Transport adapter
  gRPC / Protobuf / HTTP
          │
          ▼
Orchestrator / Agent Host
          │
          ▼
Agent Core
          │
          ├── Model contract → Router → Provider adapter
          └── Tool contract → capability adapters
```

Infrastructure is optional to Core and need not be reimplemented when an application already provides it.

## 2. Core technology

```text
Go
context.Context
small interfaces
ordinary typed errors
goroutines and sync primitives
bounded internal channels
JSON Schema for Tool inputs
```

Core packages must not import Protobuf, gRPC, Kubernetes, cache, database, or provider SDK packages.

## 3. Model layer

```text
Model interface
      ▼
Model Router
  model selection · fallback · rate limit · cost policy
      ▼
Provider Adapter
      ▼
external provider
```

The Router and adapters can be used by Embedded and Hosted applications. Provider-specific encoding, authentication, and transport stay below the provider-neutral Core contract.

Run-level retry remains a Core policy because it must preserve one Run and one terminal Event. Provider-local connection retry may stay in the adapter when it does not change Core semantics.

## 4. Transport

Protobuf and gRPC are optional transport boundaries for Hosted mode:

```text
protobuf command → mapper → Host operation
Core Event → projector → protobuf RunEvent
```

Generated types must not enter Core signatures. An existing Go service may mount the gRPC adapter beside its own APIs without deploying a separate Gotato infrastructure stack.

## 5. Orchestration

The Host uses Go contexts, bounded queues, synchronization, and worker coordination for:

```text
multiple streams and Runs
Agent factory and conversation ownership
admission and concurrency
cache leases
Event projection and delivery
readiness and drain
```

No channel is part of the public Core or Transport API. Every internal goroutine has an owning Context and settlement boundary.

## 6. Infrastructure integration

Infrastructure may provide:

```text
Gateway / Ingress
Kubernetes Service and Deployment
load balancing and session affinity
identity and secrets
persistent state
resource limits and autoscaling
```

For the initial PoC, use one Host process in one Pod. A Gateway must support long-lived bidirectional gRPC streams and must not retry an active Run in a way that duplicates commands.

Multi-Pod Conversation continuity is intentionally out of scope and reserved as a future ownership/routing design. Kubernetes Service routing alone does not guarantee it.

## 7. Observability

Core and Host expose structured correlation such as:

```text
Run ID · Turn · Tool Call ID · Routine ID · child Run ID
request ID · stream ID · Agent name · terminal status
```

Secrets, credentials, raw prompts, and unrestricted Tool payloads are redacted before export. Observability must not change execution semantics.

## 8. Package direction

A possible repository layout is:

```text
core/              Agent, Run, Loop, Events, Context
model/             Model contract and provider-neutral values
tool/              Tool, ToolSet, Schema helpers
routines/          child Runs and Groups
orchestration/     Host, admission, routing, cache, delivery
transport/grpc/    Protobuf mapping and gRPC server/client
adapters/          providers and capabilities
infra/             deployment examples and integration assets
```

The exact packages may evolve. Dependency direction may not: Host and adapters depend on Core contracts; Core does not depend on them.

## 9. Testing

Core uses deterministic fakes and no network. Host uses in-process transport, slow consumers, fake clocks, and race tests. Infrastructure and real provider tests are separate integration suites and must not be prerequisites for Core acceptance.
