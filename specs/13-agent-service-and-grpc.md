# 13. Agent Service and gRPC

**Status:** Required phase-two specification

> One standard service layer makes the canonical Agent runtime available to Go microservices.

## 1. Deliverables

Phase two MUST provide:

```text
Agent factory contract
bounded in-service Agent cache
service preset with safe defaults
first-party Protobuf service
gRPC server adapter
gRPC Go client
bounded Event transport
readiness and graceful drain
Kubernetes deployment baseline
```

## 2. Dependency direction

```text
application
    │
    ▼
Agent service preset
    │
    ├──► Agent factory / cache
    ├──► gRPC transport
    │
    ▼
canonical Agent API
    │
    ├──► Model
    ├──► ToolSets
    └──► Agent Routines
```

## 3. Agent factory

```go
type AgentFactory interface {
    NewAgent(context.Context, AgentRequest) (*gotato.Agent, error)
}
```

The final naming MAY evolve. The contract MUST create or restore isolated Agent state for a service request.

The preset MUST support deterministic registration of named factories. Factory construction MUST make Models, ToolSets, Extensions, Routine limits, and Run limits explicit.

## 4. Agent cache

The preset MUST support ephemeral Agents and conversation-scoped Agents through a bounded cache.

A cache entry contains one stateful Agent and lifecycle metadata:

```text
conversation key
Agent
created and last-used time
active lease count
expiration state
```

The default in-memory cache MUST provide:

```text
maximum entries
idle TTL
per-key creation coordination
active-Run pinning
idle-only eviction
explicit reset
a fake-clock testing path
cache metrics
```

Cache misses call the registered Agent factory. A cache hit returns the existing conversation Agent after admission confirms that it can accept the request.

The Pod-local cache is a runtime optimization rather than durable state. An external state provider MAY later restore Agents across Pods.

## 5. Preset behavior

```text
request validation
Agent name resolution
Agent creation or cache lookup
Run admission
Event forwarding
Context propagation
service error mapping
readiness
shutdown drain
```

Each behavior SHOULD have a coherent default and a focused replacement interface.

## 6. gRPC service

```proto
service AgentService {
  rpc Run(stream RunCommand) returns (stream RunEvent);
}
```

The attached Run protocol MUST support:

```text
Start with Agent name, conversation key, and initial Prompt
Steer
FollowUp
Cancel
```

The server MUST stream portable projections of canonical Core Events, including ToolSet and Agent Routine lifecycle Events.

## 7. Command protocol

- `Start` MUST be the first command.
- One stream MUST own one attached Run.
- Duplicate `Start` MUST produce a protocol error.
- Steering and Follow-up MUST preserve accepted order.
- Cancel MUST be idempotent.
- The terminal Run Event closes command acceptance.

## 8. Cancellation

```text
client Context
stream lifetime
explicit Cancel
Pod drain deadline
      │
      ▼
Run Context
  ├──► Model stream
  ├──► Tool executions
  └──► Agent Routines
```

Stream cancellation MUST cancel its attached Run. Server drain MUST stop new admission and apply the configured completion or cancellation policy to active Runs and their Routine trees.

## 9. Events and backpressure

The transport bridge MUST use bounded buffering. Lifecycle Events and final Tool and Routine Results MUST retain order and delivery priority.

The bridge MUST document one slow-consumer policy built from:

```text
producer backpressure with Context cancellation
coalescing optional progress updates
terminal ResourceExhausted error
```

## 10. Error mapping

```text
invalid input       → InvalidArgument
unknown Agent       → NotFound
Agent busy          → FailedPrecondition
admission limit     → ResourceExhausted
cancelled           → Canceled
deadline exceeded   → DeadlineExceeded
Model unavailable   → Unavailable
internal invariant  → Internal
```

Tool and Routine failures remain Events and Results when the parent Run continues.

## 11. Kubernetes lifecycle

The service MUST expose:

```text
Liveness()   process health
Readiness()  new-Run admission state
Drain(ctx)   transition and active-Run settlement
```

Exact Go signatures MAY evolve. Kubernetes probes and shutdown handling MUST be implementable directly from these signals.

## 12. Deployment baseline

```text
Kubernetes Service
       │
       ▼
replicated Gotato Pods
       │
       ├──► Pod-local Agent cache
       ├──► Model endpoint
       └──► capability services through ToolSets
```

Each attached Run remains on its serving Pod. Replicas provide admission capacity. Session affinity MAY improve Pod-local conversation-cache hits. Durable resume uses a separate state capability.

## 13. HTTP projection

An official HTTP/SSE or Connect-style adapter MAY project the same Agent factories, cache, commands, Events, errors, and lifecycle semantics.

## 14. Presentation

The service API exposes runtime state through portable Events. External products map those Events into their own presentation and interaction models.

## 15. Acceptance

Tests MUST prove:

- a client completes a text-only attached Run;
- Tool, ToolSet, and Routine Events cross the stream in canonical order;
- Steering and Follow-up reach the active Agent;
- client cancellation reaches Model, Tool, and Routine fakes;
- concurrent conversations receive isolated Agent state;
- same-Pod conversation cache hits restore the same idle Agent;
- active cache entries remain pinned;
- TTL and capacity eviction apply only to idle entries;
- admission and Event buffering are bounded;
- readiness changes during drain;
- drain applies the configured active-Run policy.
