# Extension Model

**Status:** Draft

> Extensions customize named runtime stages without changing the Agent service protocol or duplicating the canonical loop.

## 1. Role

```text
Tool / ToolSet  → capability available to the Model
Extension       → behavior at a runtime lifecycle joint
Adapter         → connection to an external technology
Service policy  → hosted access and process lifecycle behavior
```

Extensions are explicit Go components installed by an Agent definition or factory. They execute inside the transport-independent runtime boundary.

```text
Agent definition
      ├── Model
      ├── ToolSets
      ├── Extensions
      └── limits
            ↓
       Agent factory
            ↓
          Agent
```

## 2. Runtime Moving Parts

```text
ContextTransformer
        │
        ▼
MessageConverter
        │
        ▼
    Model Turn ───────────────► EventObserver
        │
        ▼
   PreToolUse
        │
        ▼
    Tool Use
        │
        ▼
  PostToolUse
        │
        ▼
   TurnStopper
```

Each Moving Part corresponds to a concrete execution stage. Core retains state transition, ordering, validation, commitment, cancellation, and settlement.

## 3. Focused capability interfaces

An Extension implements only the capabilities it needs:

```go
type ContextTransformer interface { /* ... */ }
type MessageConverter interface { /* ... */ }
type PreToolUse interface { /* ... */ }
type PostToolUse interface { /* ... */ }
type EventObserver interface { /* ... */ }
type TurnStopper interface { /* ... */ }
```

The exact public signatures follow the runtime contracts proven by hosted Agent behavior. Interface responsibilities remain narrow and independently testable.

## 4. Context transformation

`ContextTransformer` receives an immutable snapshot and produces the Messages used by the next Model Turn.

It can:

```text
select transcript ranges
add application context
prune stale material
compact earlier Turns
apply a context budget
```

It receives the Run Context and cannot mutate committed Agent history implicitly.

## 5. Message conversion

`MessageConverter` maps transformed Gotato Messages into the portable representation consumed by the Model adapter.

```text
Agent transcript
      ↓ ContextTransformer
selected runtime Messages
      ↓ MessageConverter
portable Model Messages
      ↓ provider adapter
provider request
```

This boundary keeps runtime history distinct from provider-specific encoding and from Protobuf transport Messages.

## 6. Pre-Tool-Use

Pre-Tool-Use runs after Tool resolution, complete argument assembly, and Schema validation.

```text
Proceed
Block with Tool Result
Attach termination hint
```

Validated arguments remain immutable through this stage. The Tool executor runs only after every installed Pre-Tool-Use component proceeds.

Typical uses include authorization decisions, approvals, audit preparation, and policy checks.

## 7. Tool execution

The Tool interface is a Moving Part. Core supplies a validated Tool Use, Run Context, and bounded progress reporter and invokes the resolved executor at most once.

```text
Pre A → Pre B → Tool Use → Post B → Post A
```

Retry behavior belongs to an explicit idempotency-aware Tool or policy adapter. Generic middleware does not replay an arbitrary Tool Use invisibly.

## 8. Post-Tool-Use

Post-Tool-Use receives every finalized outcome, including executed and blocked outcomes:

```text
model-facing Result
application metadata
safe Cause
Executed
Blocked
termination hint
```

It can normalize content, add typed metadata, redact fields, apply result bounds, and attach a termination hint before the final Event and transcript commitment.

The outcome always records whether the Tool executor ran.

## 9. Event observation

An `EventObserver` receives canonical runtime Events in production order. Its behavior explicitly selects blocking or advisory failure semantics.

```text
Canonical Event
      ├── blocking observer
      ├── advisory observer
      ├── test recorder
      └── telemetry adapter
```

Observers do not mutate canonical Event identity, kind, order, or correlation.

An observer is awaited before the loop proceeds, which gives an in-process consumer exact ordering without buffering. That same property makes an observer the wrong place for remote work:

```text
An observer is in-process, fast, and Context-aware.
An observer does not block on a network peer, on a remote lock,
or on any wait that has no bound of its own.
```

Remote consumers receive Events across a bounded service boundary instead.

## 10. Event projection and delivery

Event consumers use focused components:

```text
Canonical Event
      ↓ EventEnricher
      ↓ EventProjector
      ↓ EventRedactor
      ↓ EventFilter
      ↓ bounded delivery
      ├── gRPC client
      ├── logs and traces
      └── application sink
```

Projection is consumer-specific. A gRPC bridge and an OpenTelemetry sink can receive different representations of the same fact without changing runtime history.

Transport delivery policies belong to the service boundary rather than runtime Extensions.

## 11. Turn stopping

`TurnStopper` runs after `turn_end` and before continuation selection:

```text
turn_end
   ↓
TurnStopper
   ↓
Steering
   ↓
Tool continuation
   ↓
Follow-up
   ↓
completion
```

It can settle a Run before another Model request while preserving the completed Turn and its Events.

## 12. Ordering

```text
Installed: A → B → C
Pre:       A → B → C
Tool Use:  at most once
Post:      C → B → A
Observers: registration order
```

Ordering is deterministic for direct and service-hosted execution.

## 13. Failure semantics

Each capability uses one explicit behavior:

```text
blocking    return an error and terminate the current operation
transform   return the value consumed by the next stage
advisory    report failure while preserving the Run outcome
```

Blocking Extension errors terminate the Run. Tool execution errors become failed Tool Results when the Runtime can continue. The service projects these outcomes without reclassifying Extension behavior.

Panic recovery boundaries protect Tools, Extensions, observers, Agent Routines, and service callbacks.

## 14. Service policies are separate Moving Parts

Hosted lifecycle customization uses service-owned contracts:

```text
AgentFactory
AgentCache
AdmissionController
EventProjector
EventBridge
ErrorMapper
DrainPolicy
```

These components operate around the Runtime. They are not runtime Extensions because they own network, process, conversation, or deployment behavior.

## 15. Composition and compatibility

Applications install Extensions through constructors and Agent definitions. Immutable Go builds provide reproducible composition. Focused packages can provide:

```text
OpenTelemetry
structured logging
context compaction
Model routing
cost accounting
authorization integration
approval integration
```

A Moving Part becomes a supported public contract when concrete service and direct-use scenarios demonstrate stable behavior and acceptance tests fix its semantics.
