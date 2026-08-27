# Runtime and Service Moving Parts

**Status:** Draft

> Service behavior discovers the contracts; fixed rails preserve one execution model; Moving Parts customize named stages.

## 1. Legend

```text
[F] Fixed Rail
    state transition, ordering, validation, commitment, cancellation

[M] Runtime Moving Part
    replaceable or composable Agent execution behavior

[C] Configurable Policy
    bounded behavior selected through configuration

[S] Service Moving Part
    replaceable hosted-access and process-lifecycle behavior

[T] Transport Adapter
    wire encoding, network streaming, and protocol compatibility
```

A Moving Part is a stable replacement boundary, not a requirement to publish every internal function as an interface.

## 2. Complete hosted path

```text
Remote Client
      │
      │ gRPC RunCommand
      ▼
┌──────────────────────────────┐
│ Protobuf / gRPC Adapter  [T] │
└──────────────┬───────────────┘
               ▼
┌──────────────────────────────┐
│ Command Receiver         [S] │
│ Start · Steer · FollowUp     │
│ Cancel                       │
└──────────────┬───────────────┘
               ▼
┌──────────────────────────────┐
│ Admission Controller     [S] │
└──────────────┬───────────────┘
               ▼
┌──────────────────────────────┐
│ Agent Resolver           [S] │
│ definition · factory · cache │
└──────────────┬───────────────┘
               ▼
┌──────────────────────────────┐
│ Agent State Coordinator  [F] │
│ one Agent · one active Run   │
└──────────────┬───────────────┘
               ▼
       Canonical Agent Loop
               │
               ▼
        Canonical Events
               │
               ▼
┌──────────────────────────────┐
│ Event Projector          [S] │
│ portable fields · redaction  │
└──────────────┬───────────────┘
               ▼
┌──────────────────────────────┐
│ Bounded Event Bridge     [S] │
│ order · coalescing · settle  │
└──────────────┬───────────────┘
               ▼
┌──────────────────────────────┐
│ Protobuf / gRPC Adapter  [T] │
└──────────────┬───────────────┘
               ▼
          Remote Client
```

The service controls hosting. The Runtime controls execution. The transport controls wire representation.

## 3. Canonical Agent loop

```text
┌─────────────────────────────┐
│ Agent State Coordinator [F] │
└─────────────┬───────────────┘
              ▼
┌─────────────────────────────┐
│ ContextTransformer      [M] │
└─────────────┬───────────────┘
              ▼
┌─────────────────────────────┐
│ MessageConverter        [M] │
└─────────────┬───────────────┘
              ▼
┌─────────────────────────────┐
│ Tool Visibility        [M/C]│
└─────────────┬───────────────┘
              ▼
┌─────────────────────────────┐
│ Model                    [M]│
└─────────────┬───────────────┘
              ▼
┌─────────────────────────────┐
│ Stream Assembler         [F]│
└─────────────┬───────────────┘
              ▼
┌─────────────────────────────┐
│ Assistant Commit         [F]│
└─────────────┬───────────────┘
              │
       ┌──────┴──────┐
       │             │
 final response   Tool Calls
       │             │
       │             ▼
       │      Tool Use Pipeline
       │             │
       └──────┬──────┘
              ▼
┌─────────────────────────────┐
│ TurnStopper              [M]│
└─────────────┬───────────────┘
              ▼
┌─────────────────────────────┐
│ Continuation Order       [F]│
│ Steering → Tools → FollowUp │
└─────────────┬───────────────┘
              │
       ┌──────┴──────┐
       ▼             ▼
   next Turn      complete
```

Service, direct Go use, and child Agent execution all converge on this loop.

## 4. Context and Model path

```text
Agent Messages
      │
      ▼
┌──────────────────────────┐
│ ContextTransformer   [M] │  select, add, prune, compact
└────────────┬─────────────┘
             ▼
┌──────────────────────────┐
│ MessageConverter     [M] │  runtime Messages → Model Messages
└────────────┬─────────────┘
             ▼
┌──────────────────────────┐
│ Tool Visibility     [M/C]│  active ToolSets → visible specs
└────────────┬─────────────┘
             ▼
┌──────────────────────────┐
│ Model                [M] │  provider-neutral stream
└────────────┬─────────────┘
             ▼
┌──────────────────────────┐
│ Stream Assembler     [F] │  text and Tool Call assembly
└────────────┬─────────────┘
             ▼
┌──────────────────────────┐
│ Transcript Commit    [F] │
└──────────────────────────┘
```

Runtime domain values remain independent of both provider SDK Messages and Protobuf transport Messages.

## 5. Tool Use path

```text
Model Tool Call
      │
      ▼
┌──────────────────────────┐
│ Assemble + Resolve   [F] │
└────────────┬─────────────┘
             ▼
┌──────────────────────────┐
│ Schema Validation    [F] │
└────────────┬─────────────┘
             ▼
┌──────────────────────────┐
│ Pre-Tool-Use         [M] │  Proceed / Block / terminate hint
└────────────┬─────────────┘
             │
       ┌─────┴─────┐
       │           │
    Proceed      Block
       │           │
       ▼           │
┌──────────────┐   │
│ Tool Use [M] │   │  executor called at most once
└──────┬───────┘   │
       └─────┬─────┘
             ▼
┌──────────────────────────┐
│ Post-Tool-Use        [M] │  normalize · redact · annotate
└────────────┬─────────────┘
             ▼
┌──────────────────────────┐
│ Final Outcome        [F] │
└────────────┬─────────────┘
             ▼
┌──────────────────────────┐
│ Tool Result Commit   [F] │  assistant source order
└──────────────────────────┘
```

Extension ordering wraps one execution attempt:

```text
A.Pre
  B.Pre
    C.Pre
      Tool Use
    C.Post
  B.Post
A.Post
```

Retry is explicit and idempotency-aware; it is not an unrestricted replay hidden inside the lifecycle.

## 6. Parallel Tool batch

```text
Assistant Tool Calls:  A · B · C
              │
              ▼
Preflight:             A → B → C
              │
              ▼
Execution:         ┌── A ──┐
                   ├── B ─────────┐
                   └── C ─────┐   │
                              │   │
Completion Events:            C → A → B
                              │
                              ▼
Transcript Commit:            A → B → C
```

```text
Preflight order        [F]
Concurrency limit      [C]
Tool implementation    [M]
Post processing        [M]
Completion Event order [F]
Transcript order       [F]
```

A gRPC client observes actual completion while the next Model Turn receives deterministic Tool Result order.

## 7. ToolSet activation

```text
Registered ToolSets
  grafana · github · kubernetes · database
                │
                ▼
       activate_toolset
                │ name = grafana
                ▼
       ordinary Tool Use path
                │
                ▼
       active state committed [F]
                │
                ▼
       next Model Turn sees
  grafana.view · edit · probe · refresh · sync
```

ToolSet descriptions and concrete Tools are Moving Parts. Activation state transition and next-Turn visibility are fixed rails.

## 8. Canonical Event path

```text
Runtime lifecycle point
          │
          ▼
┌──────────────────────────────┐
│ Canonical Event Creation [F] │
│ kind · order · correlation   │
└──────────────┬───────────────┘
               ▼
┌──────────────────────────────┐
│ Event Dispatcher         [F] │
│ observer order · barrier     │
└──────────────┬───────────────┘
               │
      ┌────────┼───────────────┐
      ▼        ▼               ▼
 Observer   Test Recorder   Service Projection
   [M]                          [S]
                                  │
                                  ▼
                        ┌──────────────────┐
                        │ Enrich       [S] │
                        │ Project      [S] │
                        │ Redact       [S] │
                        │ Filter       [S] │
                        └────────┬─────────┘
                                 ▼
                        Bounded Bridge [S]
                          │           │
                          ▼           ▼
                       gRPC       logs / OTel
```

```text
Fixed facts
  kind · production point · ordering · correlation · terminal barrier

Consumer behavior
  enrichment · projection · redaction · filtering · buffering · sink
```

Consumer processing never deletes or reorders the canonical runtime history.

## 9. Delivery boundary

```text
Runtime observer
      │ awaited · local · fast
      ▼
─────── execution settlement  [F] ───────
      │
      ▼
Event Projector                     [S]
      │
      ▼
bounded queue                       [S]
  ├── coalesce optional progress    [C]
  ├── block within a bound          [C]
  └── terminate a slow consumer     [C]
      │
      ▼
gRPC sender                         [T]
      │
      ▼
─────── delivery settlement   [S] ───────
```

Two settlements sit on this path and have different owners. Execution settlement is a fixed rail: the Run is over and owns no further work. Delivery settlement is a service policy: the consumer has received everything it will receive.

What may be dropped is fixed; how much is dropped is policy:

```text
Protected     lifecycle transitions · settled outcomes · agent_end
Coalescable   message_update · tool_execution_update · Routine progress
```

Keeping the two settlements separate is what prevents a slow client from becoming unbounded runtime state.

## 10. Agent Routine spawn

```text
Parent Model
      │ Tool Call: spawn_agent
      ▼
Pre-Tool-Use
      │
      ▼
┌──────────────────────────┐
│ Spawn Agent Routine  [M] │
│ factory · child limits   │
└────────────┬─────────────┘
             ▼
      child executor
             │
             ▼
┌──────────────────────────┐
│ Child Agent              │
│ own state · canonical Run│
└────────────┬─────────────┘
             │
       ┌─────┴──────────┐
       ▼                ▼
  Child Model       Child Tools
       │                │
       └─────┬──────────┘
             ▼
       Routine Result
             │
             ▼
Post-Tool-Use
             │
             ▼
Parent Tool Result
             │
             ▼
Parent Model continues
```

Context ownership:

```text
service stream Context
          ↓
Parent Run Context
      ├── Parent Model and Tools
      └── Routine Context
              ├── Child Model
              ├── Child Tools
              └── Nested Routines
```

## 11. Routine Group

```text
                      Parent Agent
                            │
                      Routine Group
                      concurrency [C]
                            │
              ┌─────────────┼─────────────┐
              ▼             ▼             ▼
          Routine A     Routine B     Routine C
              │             │             │
              └─────────────┼─────────────┘
                            ▼
                    Coordination Policy [C]
              collect all · fail fast · partial · first
                            │
                            ▼
                  spawn-ordered Results
```

Completion Events retain actual completion order; result aggregation remains deterministic.

## 12. Agent resolution path

```text
Start Command
      │
      ▼
┌──────────────────────────┐
│ Request Validator    [S] │
└────────────┬─────────────┘
             ▼
┌──────────────────────────┐
│ AdmissionController  [S] │
└────────────┬─────────────┘
             ▼
┌──────────────────────────┐
│ Agent name resolver  [S] │
└────────────┬─────────────┘
             ▼
     conversation key?
        ┌────┴────┐
        │         │
       no        yes
        │         │
        ▼         ▼
 AgentFactory   AgentCache
     [S]           [S]
        │         │
        └────┬────┘
             ▼
       isolated Agent
             │
             ▼
      acquire Run ownership
```

The service owns conversation continuity and cache leases. The Runtime owns Agent state consistency and busy detection.

## 13. Cancellation path

```text
Client Cancel ─────────────┐
Stream Context done ───────┤
Run deadline ──────────────┤
DrainPolicy deadline ──────┘
                           │
                           ▼
                     Run Context [F]
                      ├── Model
                      ├── Tools
                      ├── observers
                      └── Agent Routines
```

All external cancellation sources converge on one runtime cancellation tree.

## 14. Drain path

```text
Drain requested
      │
      ▼
Readiness false       [S]
      │
      ▼
New admission stops   [S]
      │
      ▼
Active Runs tracked   [S]
      │
      ├── complete within deadline
      └── DrainPolicy cancels remaining
      │
      ▼
Event bridges settle
      │
      ▼
process exits
```

Drain changes service lifecycle, not canonical Run semantics.

## 15. Direct Go path

```text
Direct Go Caller
      │ Prompt / Continue / Cancel
      ▼
Runtime API
      │
      ▼
Canonical Agent Loop
      │
      ▼
Event subscription + RunResult
```

This path removes service and transport components while retaining identical runtime rails. It is the second consumer used to validate which runtime contracts deserve a public library commitment.

## 16. Moving Part inventory

### Runtime Moving Parts

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
Agent Routine factory and executor
```

### Runtime policies

```text
Tool execution mode
Tool batch failure policy
ToolSet visibility and activation limits
Routine concurrency and depth
local Run limits
observer failure mode
```

### Service Moving Parts

```text
AgentFactory
AgentCache
AdmissionController
EventProjector
EventBridge
ErrorMapper
DrainPolicy
```

### Transport adapters

```text
Protobuf command mapper
Protobuf Event projector
gRPC server stream
gRPC Go client
optional HTTP projection
```

### Internal fixed or test-injectable components

```text
StreamAssembler
SchemaValidator
ToolResolver
EventDispatcher
state commitment
Clock
```

Public API status is separate from architectural importance. Contracts become public after their behavior is proven across the hosted service and a direct runtime consumer.
