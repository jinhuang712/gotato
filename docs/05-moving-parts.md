# Boundaries and Moving Parts

**Status:** Draft

> This document maps the boundaries between Agent Core, Orchestration, Transport, adapters, and Infrastructure.

## 1. Boundary map

```text
┌─────────────────────────────────────┐
│ Infrastructure                       │
│ Gateway · K8s · LB · storage        │
└──────────────────┬──────────────────┘
                   ▼
┌─────────────────────────────────────┐
│ Transport goroutines                │
│ gRPC · Protobuf · HTTP projection   │
└──────────────────┬──────────────────┘
                   ▼
┌─────────────────────────────────────┐
│ Orchestration goroutines             │
│ admission · queues · routing        │
│ coordination · delivery · drain     │
└──────────────────┬──────────────────┘
                   ▼ channels
┌─────────────────────────────────────┐
│ Agent goroutine / Core              │
│ private state · canonical Loop      │
│ Events · cancellation · limits     │
└──────────────────┬──────────────────┘
                   ▼ explicit contracts
┌─────────────────────────────────────┐
│ Model and capability adapters       │
│ providers · DB · Redis · HTTP · MCP │
└─────────────────────────────────────┘
```

These may be packages in one Go process or separately connected components. Agent Core remains a goroutine-backed execution unit; surrounding layers communicate with it through explicit contracts rather than taking over its work.

## 2. Core moving parts

```text
Agent goroutine
Model
ContextTransformer
MessageConverter
Tool
ToolSet
PreToolUse
PostToolUse
TurnStopper
EventObserver
Agent Routine spawn capability
```

Core fixed rails retain private state mutation, Model stream assembly, validation, transcript commitment, Event order, cancellation, local limits, and terminal settlement.

## 3. Orchestration moving parts

```text
Agent Registry / Factory
Agent Handle / Channel Router
Admission Controller
Request Queue / Scheduler
Dispatch and Preemption Policy
Event Projector
Bounded Event Bridge
Error Mapper
Drain Policy
```

These components create and coordinate Agent goroutines. They decide how external requests are handled when an Agent is Busy. They cannot edit Agent state or reproduce the Model/Tool Loop.

## 4. Transport adapters

```text
Protobuf command mapper
Protobuf Event projector
gRPC server stream
gRPC client
optional HTTP/SSE or Connect projection
```

Transport changes representation and protocol behavior. It does not change Agent semantics or provide a second Loop.

## 5. Infrastructure adapters

```text
Gateway / Ingress
Kubernetes Service and Deployment
load balancing and session affinity
secrets and identity
persistent state
resource and autoscaling configuration
```

Infrastructure is external to Agent Core and may already be provided by the application platform. It routes and hosts processes; it does not define Agent state or Loop semantics.

## 6. Model layer

```text
Agent Model interface
      ▼
Model Router
  selection · fallback · rate limit · cost policy
      ▼
Provider Adapter
      ▼
external Model provider
```

Provider-specific retries may happen inside an adapter. Run-level retry remains a Core policy because it must preserve one Run and one terminal Event.

## 7. Capability layer

Applications expose external systems through explicit Tool and ToolSet adapters:

```text
DB / Redis / HTTP / gRPC / MCP / workflow / sandbox
                         ▼
                    Tool contract
                         ▼ channel-backed invocation
                    Agent goroutine
```

The adapter owns authentication, external timeout mapping, and private diagnostics. Agent Core owns Tool identity, validation, invocation boundaries, Events, and result commitment.

## 8. Embedded composition

```text
Application goroutine
  ├── caller-owned request policy
  ├── Model Router
  ├── Tools
  └── Agent goroutine
```

No Host or Transport is required. The application can send one Prompt and wait, or provide its own queue and dispatch policy.

## 9. Hosted composition

```text
Client
  ↓
Transport goroutines
  ↓ channels
Orchestration goroutines
  ↓ Agent command channel
Agent goroutine
```

The Host adds remote admission, request policy, routing, bounded delivery, cancellation mapping, and lifecycle. It does not become the Agent.

## 10. Fixed rails versus policy

Fixed Core semantics:

```text
Agent private state and one Loop
one current Prompt/Continue per Agent goroutine
canonical Event sequence
Tool validation and commitment
one terminal Event per Run
Agent Context and cancellation points
```

Caller/Host policy:

```text
external request queue and queue sizes
reject / wait / priority / preemption
number of Agent goroutines
Conversation routing
Event coalescing and queue-full policy
Group coordination
```

A policy must not alter the meaning of an Agent identity, transcript, Event sequence, or terminal Run result.
