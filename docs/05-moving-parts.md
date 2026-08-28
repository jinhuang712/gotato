# Runtime, Orchestration, and Infrastructure Boundaries

**Status:** Draft

> A replacement boundary belongs to the layer that owns its state and failure semantics.

## 1. Boundary map

```text
┌─────────────────────────────────────┐
│ Infrastructure                       │
│ Gateway · K8s · LB · storage        │
└──────────────────┬──────────────────┘
                   ▼
┌─────────────────────────────────────┐
│ Transport                           │
│ gRPC · Protobuf · HTTP projection   │
└──────────────────┬──────────────────┘
                   ▼
┌─────────────────────────────────────┐
│ Orchestration / Agent Host          │
│ admission · routing · cache         │
│ concurrency · delivery · drain      │
└──────────────────┬──────────────────┘
                   ▼
┌─────────────────────────────────────┐
│ Agent Core                          │
│ canonical loop · state · Events     │
└──────────────────┬──────────────────┘
                   ▼
┌─────────────────────────────────────┐
│ Model and Capability Adapters       │
│ providers · DB · Redis · HTTP · MCP │
└─────────────────────────────────────┘
```

These may be packages in one Go process or separately deployed components. The dependency direction remains inward toward Core contracts.

## 2. Core Moving Parts

```text
Model
ContextTransformer
MessageConverter
Tool
ToolSet
PreToolUse
PostToolUse
TurnStopper
EventObserver
Agent Routine factory/executor
```

Core fixed rails retain state mutation, stream assembly, validation, commitment, Event order, cancellation, limits, and terminal settlement.

## 3. Orchestration Moving Parts

```text
Agent Registry / Factory
Conversation Owner / Resolver
Agent Cache / Lease
Admission Controller
Run and stream coordinator
Event Projector
Bounded Event Bridge
Error Mapper
Drain Policy
```

These components coordinate Core instances. They must call Core APIs rather than edit transcript state or reproduce the Model/Tool loop.

## 4. Transport adapters

```text
Protobuf command mapper
Protobuf Event projector
gRPC server stream
first-party Go client
optional HTTP/SSE or Connect projection
```

Transport changes representation and protocol behavior; it does not change Core semantics.

## 5. Infrastructure adapters

```text
Gateway / Ingress
Kubernetes Service and Deployment
load balancing and session affinity
secrets and identity
persistent state
resource and autoscaling configuration
```

Infrastructure is optional to Core and may already be provided by the application platform. It routes and hosts processes; it does not own Agent state or Agent Loop semantics.

## 6. Model layer

```text
Core Model interface
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
                         ▼
                      Agent Core
```

The adapter owns authentication, external timeouts, and private diagnostics. Core owns Tool identity, validation, execution boundaries, and result commitment.

## 8. Embedded composition

```text
Application
  ├── business orchestration
  ├── Model Router
  ├── Tools
  └── Agent Core
```

No Host or Transport is required. The application can provide its own concurrency, routing, and lifecycle policy.

## 9. Hosted composition

```text
Client → Transport → Agent Host → Agent Core
                         │
                         └── Model / Capability adapters
```

The Host adds only the semantics required by remote multi-client access: admission, ownership, bounded delivery, cancellation mapping, and lifecycle.

## 10. Fixed rails versus policy

Fixed:

```text
Core state transitions
canonical Event sequence
Tool validation and commitment
one terminal Event
Context ownership
```

Configurable:

```text
Host concurrency and queue sizes
cache TTL and capacity
Event coalescing and queue-full policy
routing and admission policy
Tool execution mode
Routine group policy
```

A configurable policy must not alter the meaning of a Core identity, transcript, Event sequence, or terminal Run result.
