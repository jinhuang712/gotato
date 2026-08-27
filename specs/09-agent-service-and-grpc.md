# 09. Agent Service and gRPC

**Status:** Draft

> The service is the product boundary. It maps a wire protocol onto the canonical runtime and owns everything the runtime deliberately does not: remote identity, hosted lifecycle, and delivery.

## 1. Deliverables

```text
Agent definition registry and factory contract
conversation-scoped Agent resolution
bounded in-process Agent cache
Run admission with typed rejection
first-party Protobuf service contract
gRPC server adapter and Go client
bounded Event delivery with a stated slow-consumer policy
remote cancellation
readiness and graceful drain
Kubernetes deployment baseline
```

## 2. Dependency direction

```text
application
    │
    ▼
service preset
    ├──► AgentFactory / AgentCache
    ├──► admission / drain
    ├──► Event projection / bridge
    │
    ▼
canonical Agent API
    ├──► Model
    ├──► Tools and ToolSets
    └──► Agent Routines
```

The service MUST call the canonical runtime API. It MUST NOT maintain a second Agent state machine, and it MUST NOT reproduce loop behavior in a handler, a cache, or a Routine executor.

## 3. Service contract

```proto
service AgentService {
  rpc Run(stream RunCommand) returns (stream RunEvent);
}
```

The bidirectional stream represents one attached Run.

The Protobuf contract is the external compatibility surface. Generated types MUST stay at the transport boundary and MUST NOT appear in runtime signatures.

## 4. Command protocol

```text
Start     → Prompt or Continue
Steer     → Steering queue
FollowUp  → Follow-up queue
Cancel    → idempotent Run cancellation
```

- `Start` MUST be the first command.
- One stream MUST own one attached Run.
- A duplicate `Start` MUST produce a protocol error.
- Steering and Follow-up MUST preserve accepted order.
- `Cancel` MUST be idempotent.
- The terminal Run Event MUST close command acceptance.
- A command after terminal settlement MUST produce a protocol error.

Command acceptance is distinct from execution effect. The service acknowledges that a command was accepted; runtime rules determine when an accepted Steering or Follow-up Message affects a Turn. An acknowledgement MUST NOT imply completion.

## 5. Agent factory

```go
type AgentFactory interface {
    NewAgent(context.Context, AgentRequest) (*gotato.Agent, error)
}
```

Final naming MAY evolve. The contract MUST create or restore isolated Agent state for a service request.

The preset MUST support deterministic registration of named factories. Factory construction MUST make Models, Tools, ToolSets, Extensions, and limits explicit.

## 6. Conversation resolution

```text
Start command
      ↓
request validation
      ↓
admission
      ↓
Agent name resolution
      ↓
conversation key present?
   ┌──┴──┐
  no     yes
   │      │
   ▼      ▼
factory  cache
   └──┬───┘
      ▼
isolated Agent
      ↓
acquire Run ownership
```

Conversation identity is a service concept. Messages and active ToolSets remain runtime state owned by the resolved Agent.

The service MUST coordinate per-key creation so concurrent first requests produce one Agent. Concurrent conversations MUST use separate Agent instances.

## 7. Agent cache

A cache entry contains one stateful Agent and its lifecycle metadata:

```text
conversation key
Agent
created and last-used time
active lease count
expiration state
```

The default in-process cache MUST provide:

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

Eviction MUST NOT remove an entry with an active Run. A cache hit MUST return the existing Agent only after admission confirms it can accept the request.

The cache is a runtime optimization, not durable truth. Durable continuity requires an explicit state provider and restoration contract.

## 8. Event delivery

```text
canonical Event
      ↓
projection and redaction
      ↓
bounded queue
      ↓
gRPC sender
```

The bridge MUST use bounded buffering and MUST declare capacity, protected Event kinds, coalescing behavior, queue-full behavior, and shutdown flush behavior.

Protected Events MUST cross the transport in canonical order or the stream MUST fail. Coalescable progress MAY be merged.

A remote consumer MUST NOT be attached as a runtime observer, because a network peer has no bound of its own and would let one client hold a Run open indefinitely.

## 9. Cancellation

```text
client Context
stream close
explicit Cancel
Run deadline
drain deadline
      │
      ▼
Run Context
  ├──► Model stream
  ├──► Tool Uses
  ├──► awaited observers
  └──► Agent Routines
```

Every cancellation source MUST converge on the Run Context.

A closed stream ends delivery. Whether it also cancels the Run is a documented service policy; for an attached Run, treating stream close as cancellation is the expected default.

## 10. Admission

```text
incoming Run
    ↓
lifecycle check
    ↓
global and per-Agent bounds
    ↓
Agent availability
    ↓
accept or typed rejection
```

Admission governs hosted capacity. Runtime limits govern one accepted Run and its child work. The two MUST remain distinct.

## 11. Error mapping

```text
invalid input       → InvalidArgument
unknown Agent       → NotFound
Agent busy          → FailedPrecondition
admission limit     → ResourceExhausted
delivery exhausted  → ResourceExhausted
cancelled           → Canceled
deadline exceeded   → DeadlineExceeded
Model unavailable   → Unavailable
internal invariant  → Internal
```

Tool and Routine failures remain Events and Results while the parent Run continues. They MUST NOT become transport errors.

Public error messages MUST be safe for transport exposure. Internal causes MAY remain available to application-controlled diagnostics.

## 12. Lifecycle signals

The service MUST expose:

```text
Liveness()   process health
Readiness()  new-Run admission state
Drain(ctx)   transition and active-Run settlement
```

Exact Go signatures MAY evolve. Cluster probes and shutdown handling MUST be implementable directly from these signals.

```text
drain requested
      ↓
readiness false
      ↓
admission stops
      ↓
active Runs settle or reach the drain deadline
      ↓
DrainPolicy handles the remainder
      ↓
bridges flush or abandon
      ↓
process exits
```

## 13. Deployment baseline

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

An attached Run MUST remain on the Pod serving its stream. Replicas provide admission capacity. Session affinity MAY improve conversation-cache locality. Durable resume requires a separate state capability.

## 14. Optional projections

An HTTP/SSE or Connect-style adapter MAY project the same factories, cache, commands, Events, errors, and lifecycle semantics. It MUST NOT introduce alternative Agent semantics.

## 15. Acceptance

Tests MUST prove:

- a client completes a text-only attached Run;
- Tool, ToolSet, and Routine Events cross the stream in canonical order;
- `Start` ordering, duplicate `Start`, and post-terminal commands behave as specified;
- Steering and Follow-up reach the active Agent in accepted order;
- client cancellation reaches Model, Tool, and Routine fakes;
- concurrent conversations receive isolated Agent state;
- a conversation cache hit returns the same idle Agent with its transcript;
- active cache entries remain pinned and TTL applies only to idle entries;
- admission and Event buffering are bounded;
- a slow consumer triggers the documented policy without dropping a protected Event;
- a slow consumer does not stall an unrelated Run;
- readiness changes during drain and DrainPolicy is applied.
