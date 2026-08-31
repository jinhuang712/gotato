# Hosted Agent Service

**Status:** Draft

> **Agent as a Service.**
>
> **Core Native to Go.**
>
> A Hosted Service connects remote callers to Agent Core through Transport and Orchestration; it does not replace Agent execution.

## 1. Scope

Hosted mode exposes Agent routines to remote clients through a channel-shaped transport:

```text
Client
  │ bidirectional Run stream
  ▼
Transport goroutines
  │ command / Event channels
  ▼
Orchestration goroutines
  │ Agent command channel
  ▼
Agent goroutine
```

The initial PoC uses one Host process in one Pod. The hosted layer validates remote access, admission, scheduling, Event projection, cancellation, and lifecycle without becoming a distributed platform.

A Go application may embed Agent Core directly without using this service.

## 2. Hosted components

```text
Transport goroutines
  Protobuf · gRPC stream · command validation · wire projection

Orchestration goroutines
  Agent registry · factory · admission · request queues
  routing · dispatch when Free · stream attachment
  cancellation policy · Event projection and bridge
  readiness · drain · channel coordination

Agent goroutines
  private state · canonical Loop · Tools
  Extensions · Agent Routines · canonical Events
```

Orchestration sends commands to Agent channels and receives results and Events through channel-backed handles. It must not maintain a parallel transcript or Loop.

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

The stream carries commands to an Agent execution and receives its projected Events. The first command is `Start`; after terminal settlement no further commands are accepted on that execution stream.

## 4. Request path

```text
RunCommand.Start
      ↓
Transport goroutine validates
      ↓
Orchestration admission / queue policy
      ↓
resolve Agent handle
      ↓
wait for Free, reject, or choose control action
      ↓
dispatch command through Agent channel
      ↓
Agent goroutine runs the canonical Loop
      ↓
Event channel → projection → bounded bridge
      ↓
RunEvent stream
```

`Steer`, `FollowUp`, and `Cancel` are control messages. Whether a new external Prompt waits, queues, is prioritized, steers the current execution, or aborts it is an Orchestration policy.

## 5. Concurrency

A Host must support concurrent remote streams under explicit bounds:

```text
Host process          multiple transport and orchestration goroutines
Agent goroutine       one Prompt or Continue at a time
Busy Agent            reject, queue, prioritize, Steer, or Abort by policy
Different Agents      may execute concurrently
One Run               bounded Tool concurrency
One client            bounded Event delivery
```

The Host owns bounds for streams, Agent routines, queued requests, dispatch, and delivery. The Agent owns only its private state and current execution. Kubernetes replica count is not a substitute for Host scheduling.

The concrete preset values remain configuration decisions; every bound must be explicit and observable.

## 6. Conversation routing in the PoC

The initial PoC runs one Host process in one Pod. A process-local registry maps a Conversation key to an Agent handle:

```text
agent_name + conversation_key
              ↓
process-local routing table
              ↓
Agent handle / command channel
```

The registry and cache retain handles and route requests. They do not make an Agent the owner of a Conversation or of Host resources. The Host decides whether a request waits for the Agent to become Free or enters a queue.

### Reserved: Multi-Pod Conversation Routing

Multi-Pod continuity is intentionally left as a separate future design area. It may require keyed routing, distributed ownership, or durable state restoration. Ordinary Kubernetes load balancing alone is not a continuity guarantee.

## 7. Event delivery

The Agent emits canonical Events through its Event channel. The Host projects them to transport Events:

```text
Agent Event → projection / redaction → bounded bridge → gRPC sender
```

Protected lifecycle and outcome Events cannot be silently dropped. Optional progress may be coalesced. A slow client may be slowed within a bound, have progress coalesced, or have its stream terminated according to policy. It must not hold unrelated Agent goroutines open.

Execution settlement belongs to the Agent Run; remote delivery settlement belongs to the Host bridge.

## 8. Cancellation

```text
client Cancel / stream Context / deadline / drain
                         ↓
             Orchestration control channel
                         ↓
                  Agent Abort
                         ↓
             Model · Tools · observers · local work
```

Stream closure ends remote delivery. The default attached-Run policy may also cancel execution, but the Host must document the choice. Cancellation of another Agent routine requires an explicit command or Host policy; spawn provenance does not imply cancellation ownership.

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
  └── readiness: new requests may be admitted

Draining
  ├── readiness false
  ├── new admission rejected
  ├── queued requests handled by policy
  ├── active Agent Runs settle or are cancelled by deadline
  └── Event bridges flush or abandon within policy
```

Infrastructure consumes these signals; it does not define Agent semantics.

## 11. Deployment forms

### Existing Go service

```text
Existing Go Service
  ├── business API goroutines
  ├── optional Orchestration goroutines
  └── Agent goroutines
```

No new infrastructure is required merely because the Core is used.

### Dedicated Hosted Service

```text
Gateway/LB → Transport goroutines → Orchestration → Agent goroutines
```

This form provides a dedicated remote Agent endpoint while retaining the same Core Agent Loop.

## 12. Ownership map

```text
Agent goroutine  private Agent state and execution
Orchestration    request policy, scheduling, routing, coordination
Transport        wire protocol and stream projection
Infrastructure   process hosting and network routing
Application      business meaning, definitions, and policies
```
