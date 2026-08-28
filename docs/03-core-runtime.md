# Core Runtime

**Status:** Draft

> The service depends on one transport-independent state owner and one canonical Agent loop.

## 1. Role

The Core Runtime is the Go execution kernel beneath the Agent service:

```text
RunCommand
    ↓ service mapping
Agent operation
    ↓
Core Runtime
  Agent state · Model/Tool loop · Events
    ↓
Canonical Event
    ↓ service projection
RunEvent
```

The Runtime is a boundary in the dependency graph. The service names runtime types; the Runtime names nothing above it.

## 2. Runtime shape

```text
                 ┌──────────────────┐
                 │      Agent       │
                 │ state + queues   │
                 └────────┬─────────┘
                          │ coordinates
          ┌───────────────┼──────────────────┐
          ▼               ▼                  ▼
    ┌──────────┐    ┌──────────┐      ┌───────────────┐
    │  Model   │    │  Tools   │      │Agent Routines │
    └──────────┘    └──────────┘      └───────────────┘
          │               │                  │
          └───────────────┼──────────────────┘
                          ▼
                   Canonical Events
```

## 3. Fixed rails and Moving Parts

```text
Fixed Rails
  state transition · stream assembly · validation · commitment
  Event order · cancellation · limits · terminal settlement

Moving Parts
  ContextTransformer · MessageConverter · Model
  Tool · ToolSet · PreToolUse · PostToolUse
  EventObserver · TurnStopper · Agent Routine factory
```

The Runtime controls when each stage runs. Moving Parts control behavior inside named stages. Configuration selects bounded policies without changing lifecycle order.

## 4. Agent state

An Agent owns:

```text
system instructions
Model
Messages
individual Tools
registered and active ToolSets
Model options
Steering queue
Follow-up queue
active Run state
```

One active Run serializes state mutation. Inspection uses immutable snapshots or equivalent read-only values.

The service owns how Agent instances are created, retained, and located. The Runtime owns the consistency of each Agent instance.

## 5. Runtime operations

The canonical runtime supports semantic equivalents of:

```text
Prompt
Continue
Steer
FollowUp
Abort
WaitForIdle
Subscribe
StateSnapshot
Reset
```

`Prompt` commits a user Message before the first Model Turn. `Continue` resumes an eligible transcript without adding a user Message. `Abort` cancels the active Run Context. `WaitForIdle` observes complete settlement.

These operations form the service-to-runtime boundary and the candidate direct Go API.

## 6. Canonical loop

```text
accept Run
emit agent_start
repeat:
  emit turn_start
  transform and convert Model context
  resolve visible Tools
  stream and assemble assistant Message
  commit assistant Message
  process requested Tool Uses
  commit Tool Results in source order
  emit turn_end
  apply TurnStopper
  apply Steering
  continue after Tool Results
  apply Follow-up when otherwise complete
until terminal outcome
emit agent_end
settle Run
```

```text
                     ┌───────────────┐
                     │  Model Turn   │◄──────────────────┐
                     └───────┬───────┘                   │
                             │                           │
                    ┌────────┴────────┐                  │
                    │                 │                  │
              final response      Tool Calls             │
                    │                 │                  │
                    ▼                 ▼                  │
                 finish       Tool Use Pipeline          │
                                      │ Results          │
                                      └──────────────────┘
```

The service invokes this loop; it never reproduces it in a gRPC handler, cache, or Routine executor.

## 7. Context and Model path

```text
Agent Messages
      ↓
ContextTransformer
      ↓
MessageConverter
      ↓
Tool Visibility
      ↓
Model Stream
      ↓
Stream Assembler
      ↓
Assistant Commit
```

Context selection, conversion, visibility, and Model behavior are composable. Stream assembly and transcript commitment remain fixed runtime rails.

Runtime Messages do not depend on provider SDK or Protobuf Message types.

## 8. Tool Use pipeline

```text
Tool Call
  → assemble arguments
  → resolve Tool
  → validate Schema
  → Pre-Tool-Use
  → Tool Use at most once
  → Post-Tool-Use
  → final Tool outcome
  → Tool Result commit
```

Pre-Tool-Use can proceed or block with a Result. Post-Tool-Use receives executed and blocked outcomes and can transform the model-facing Result before commitment.

Sequential mode preserves source order throughout. Bounded parallel mode emits completion Events as work finishes and commits Tool Results in assistant source order.

## 9. ToolSet activation

Inactive ToolSets appear through a built-in activation Tool:

```text
activate_toolset("grafana")
      ↓
ordinary Tool Use pipeline
      ↓
active ToolSets updated
      ↓
next Model Turn sees Grafana Tools
```

Activation is idempotent, bounded, stateful, and observable.

## 10. Agent Routines

An Agent Routine composes a child Agent through the same runtime boundary:

```text
Parent Run
    │ spawn
    ▼
Agent Routine
    ├── child Agent
    ├── child Run
    ├── child Context
    └── Routine Result
```

A `spawn_agent` Tool can expose this composition to a parent Model. Parent cancellation reaches the complete child tree. A Routine executor never introduces a distinct child loop.

## 11. Steering, Follow-up, and stopping

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
complete
```

Steering Messages enter after the current Tool batch. Follow-up Messages enter when the Run would otherwise complete. This order is a fixed rail shared by direct and remote callers.

## 12. Canonical Events

Runtime Events describe facts:

```text
Agent and Turn lifecycle
Message streaming
Tool execution
ToolSet activation
Agent Routine lifecycle
terminal Run settlement
```

The Runtime fixes Event production points, order, correlation, and terminal barriers. Enrichment, projection, redaction, coalescing, transport buffering, and sinks operate on consumer-specific views.

Two classes exist, and the distinction governs what a consumer may drop:

```text
Protected      lifecycle transitions and settled outcomes
Coalescable    optional Message, Tool, and Routine progress
```

`agent_end` is the final canonical Run Event and the only completion signal a caller needs. Retry, context compaction, and queued continuation happen inside the Run, so nothing resumes execution after it.

### Who may hold the loop

An observer runs inside the Run and is awaited before the loop proceeds. That is deliberate: an in-process consumer gets exact ordering and natural pacing for free.

```text
Canonical Event
      ↓
observer runs
      ↓
loop continues
```

The privilege is bounded by who may claim it:

```text
An observer is in-process, fast, and Context-aware.
An observer does not block on a network peer, on a remote lock,
or on any wait that has no bound of its own.
```

Remote consumers do not attach here. The service reads canonical Events and carries them across its own bounded boundary, so a slow client changes delivery timing without changing execution timing.

```text
Runtime                        Service
───────                        ───────
awaited observer               bounded queue
local · fast · cancellable      slow-consumer policy
```

## 13. Cancellation tree

Every Run owns a `context.Context`:

```text
Run Context
  ├── ContextTransformer and MessageConverter
  ├── Model stream
  ├── Tool Uses
  ├── awaited Extensions and observers
  └── Agent Routines
```

The service stream, explicit Cancel, deadline, or drain policy can cancel the Run Context. Runtime cancellation then reaches every owned child operation.

## 14. Limits

```text
Turns and Tool Calls
active ToolSets and visible Tools
parallel Tool Uses
Tool Result and progress volume
Agent Routine count and nesting depth
Run, Model, Tool, and Routine deadlines
```

Limits apply before new work is admitted and produce typed Tool, Routine, or Run outcomes. Service admission and shared quotas remain outside the runtime boundary.

## 15. Concurrency and settlement

One Agent has one active mutating Run. Parallel Tool Uses and Agent Routines execute with explicit bounds. Concurrent conversations use separate Agent instances selected by the service.

A terminal Run starts no new work. Execution settlement covers runtime-owned child operations and awaited observers, and it is the only settlement the Runtime defines.

Whether a remote consumer actually received the stream is delivery settlement. It belongs to the service, and it can finish later, finish earlier, or never finish without changing what the Run did.

## 16. Error model

```text
Tool execution failure
  → failed Tool Result when Model reasoning can continue

Routine failure
  → settled Routine Result when parent policy can continue

Model protocol failure, cancellation, invariant failure,
blocking Extension failure, or exhausted Run limit
  → terminal Run outcome
```

Runtime errors use typed Go categories. Service adapters map those categories to portable error details and gRPC status without changing their meaning.

## 17. What belongs in the Runtime

A component belongs in the Runtime when:

1. every Agent execution needs its semantics;
2. it remains meaningful without a network or process host;
3. the service can call it through ordinary Go values;
4. deterministic fakes can test it without provider or transport dependencies.

The Runtime does not own:

```text
Protobuf envelopes
gRPC streams
conversation routing
Agent cache leases
service admission
readiness and drain
Kubernetes resources
end-user presentation
```
