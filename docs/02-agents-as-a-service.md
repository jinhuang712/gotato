# Hosted Agent Service

**Status:** Draft

> Agent-as-a-Service is a first-class hosted composition of Transport, Orchestration, and Agent Core. The Core remains the primary execution boundary; Hosted mode is the remote delivery form, not a different Agent execution model.

## 1. Scope

Hosted mode makes Core Agents available to remote clients. The initial PoC deliberately uses one Host process in one Pod; the hosted layer is being validated as a thin composition around Core, not as a distributed platform:

```text
Client
  │ RunCommand stream
  ▼
Transport Adapter
  │
  ▼
Agent Host / Orchestrator
  │
  ▼
Agent Core
```

A Gateway, Kubernetes Service, or load balancer may surround this path, but those are infrastructure choices. A Go application may also embed the Core without using this service.

## 2. Hosted components

```text
Transport
  Protobuf · gRPC stream · command validation · wire projection

Orchestrator
  Agent registry · factory · admission · concurrency
  conversation ownership · cache/lease · stream attachment
  cancellation mapping · Event projection and bridge
  readiness · drain

Agent Core
  Agent state · Run loop · Tools · Routines · canonical Events
```

The Orchestrator invokes Core operations. It must not maintain a parallel transcript or loop.

## 3. Service contract

The first transport is a bidirectional gRPC stream:

```proto
rpc Run(stream RunCommand) returns (stream RunEvent);
```

Commands are:

```text
Start
Steer
FollowUp
Cancel
```

The stream attaches to one Run. The first command is `Start`; after terminal settlement no further commands are accepted.

## 4. Request path

```text
RunCommand.Start
      ↓
validate command
      ↓
admission
      ↓
resolve Agent name
      ↓
resolve Conversation ownership when present
      ↓
attach one Core Agent Run
      ↓
project canonical Events
      ↓
bounded Event bridge
      ↓
RunEvent stream
```

`Steer`, `FollowUp`, and `Cancel` are mapped to Core operations. Command acceptance and execution effect are separate: accepting a command does not mean that the next Model call has already observed it.

## 5. Concurrency

A Host must support concurrent remote streams under explicit bounds:

```text
Host process          multiple attached streams
Different conversations concurrent Core Runs
Same Agent             one mutating Run at a time
Same Agent, second Run typed busy rejection
One stream             commands accepted in arrival order
One Run                bounded Tool and Routine concurrency
One client             bounded Event delivery
```

The Host owns global admission such as maximum active Runs, streams, queued requests, and per-Agent capacity. Core owns single-Agent mutation and Run-local limits. Kubernetes replica count is not a substitute for either.

The concrete preset values remain configuration decisions; the contract requires that each bound be explicit and observable.

## 6. Conversation ownership in the PoC

The initial PoC runs one Host process in one Pod. Conversation ownership is therefore process-local:

```text
agent_name + conversation_key
              ↓
process-local owner / cache
              ↓
Core Agent
```

The Host may coordinate creation, pin an active Agent, and evict only idle Agents. No cross-Pod continuity is promised or required for this phase.

### Reserved: Multi-Pod Conversation Ownership

Multi-Pod continuity is intentionally left as a separate future design area. It may require keyed routing, distributed ownership, or durable state restoration. Ordinary Kubernetes load balancing alone is not a continuity guarantee.

## 7. Event delivery

The Core emits canonical Events. The Host maps them to transport Events:

```text
Core Event → projection/redaction → bounded bridge → gRPC sender
```

Protected lifecycle and outcome Events cannot be silently dropped. Optional progress may be coalesced. A slow client may be slowed within a bound, have progress coalesced, or have its stream terminated according to the configured policy. It must not hold unrelated Runs open.

Execution settlement belongs to Core; delivery settlement belongs to Host.

## 8. Cancellation

```text
client Cancel / stream Context / deadline / drain
                         ↓
                  Host Run Context
                         ↓
                    Core Abort
                         ↓
       Model · Tools · observers · child Routines
```

Stream closure ends remote delivery. The default attached-Run policy is to cancel the Run, but the Host must make the policy explicit. Cancellation is idempotent while the Run is active.

## 9. Gateway and infrastructure

Gateway and load balancer are external:

```text
Client → Gateway → LB/Kubernetes Service → Host Pod
```

They may handle TLS, authentication, network policy, and endpoint routing. They must support long-lived bidirectional gRPC streams and must not transparently retry an active Run in a way that duplicates `Start`.

The Host needs no Gotato-specific Gateway when the application already has one. Gotato provides health, drain, and transport contracts for integration.

## 10. Lifecycle

```text
Serving
  ├── liveness: process is operating
  └── readiness: new Runs may be admitted

Draining
  ├── readiness false
  ├── new admission rejected
  ├── active Runs settle or are cancelled by deadline
  └── Event bridges flush or abandon within policy
```

Infrastructure consumes these signals; it does not define Core semantics.

## 11. Deployment forms

### Existing Go service

```text
Existing Go Service
  ├── existing business API
  ├── existing Gateway / K8s deployment
  └── Agent Core or embedded Host
```

No new infrastructure is required merely because the Core is used.

### Dedicated Hosted Service

```text
Gateway/LB → Gotato Host → Agent Core
```

This form provides a dedicated remote Agent endpoint and may use the standard Orchestrator, gRPC adapter, and deployment examples.

## 12. Ownership

```text
Core          execution and canonical facts
Orchestrator  hosted coordination and remote delivery
Transport     wire protocol
Infrastructure deployment and network routing
Application   business meaning, definitions, and policies
```
