# Technology Stack and Deployment Boundaries

**Status:** Draft

> **Agent as a Service, native to Go.** Build Agent goroutines and their channel boundaries first; add protocol adapters for remote access and use the existing platform for deployment.

## 1. Stack map

The Core is the primary implementation target. The following diagram describes the optional hosted path, not a requirement that every deployment include all layers:

```text
Existing platform
  Gateway · LB · Kubernetes · storage · secrets
          │ hosts / routes
          ▼
Transport goroutines
  gRPC / Protobuf / HTTP
          │ channels
          ▼
Orchestration goroutines
  admission · queues · routing · coordination
          │ Agent command channels
          ▼
Agent goroutine / Core
  private state · canonical Loop · Events
          │
          ├── Model contract → Router → Provider adapter
          └── Tool contract → capability adapters
```

Infrastructure is optional to Core and need not be reimplemented when an application already provides it.

## 2. Core technology

```text
Go
context.Context
Agent goroutines
explicit command / result / Event channels
small interfaces
ordinary typed errors
bounded capability workers
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

Orchestration uses Go contexts, goroutines, channels, bounded queues, synchronization, and worker coordination for:

```text
multiple streams and Agent routines
Agent factory and process-local routing
admission and request queue policy
dispatch when Agent routines are Free
Event projection and delivery
readiness and drain
```

The public API may hide raw channels behind Agent handles, but channel-backed communication is the runtime model. Every long-lived goroutine has an explicit lifetime and shutdown signal; no goroutine is fire-and-forget.

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
Agent ID · Run ID · Turn · Tool Call ID · Spawn ID
request ID · stream ID · Agent name · terminal status
```

Secrets, credentials, raw prompts, and unrestricted Tool payloads are redacted before export. Observability must not change execution semantics.

## 8. Package direction

A possible repository layout is:

```text
core/              Agent goroutine, Run, Loop, Events, Context
model/             Model contract and provider-neutral values
tool/              Tool, ToolSet, Schema helpers
routine/           Agent routine handles and spawn protocol
orchestration/     scheduling, admission, routing, queues, delivery
transport/grpc/    Protobuf mapping and gRPC server/client
adapters/          providers and capabilities
infra/             deployment examples and integration assets
```

The exact packages may evolve. Dependency direction may not: Host and adapters depend on Core contracts; Core does not depend on them.

## 9. Testing

Core uses deterministic fakes and no network. Agent routine tests use scripted channels and fake clocks. Host uses in-process transport, slow consumers, request queues, and race tests. Infrastructure and real provider tests are separate integration suites and must not be prerequisites for Core acceptance.
