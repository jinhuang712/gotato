# 09. Agent Host and gRPC

**Status:** Draft

> **Agent as a Service connects clients to Agent Core through Transport and Orchestration.** The initial PoC is single-Pod; Gateway and Kubernetes are external infrastructure.

## 1. Hosted deliverables

The Hosted layer is a first-class composition and validation target, not a second Agent runtime. It creates, routes, schedules, and observes Agent goroutines through channels. The initial PoC uses one Host process in one Pod.

Hosted mode provides:

```text
Agent definition registry and factory
Host admission and concurrency
process-local Agent routing and optional handle cache
transport stream attachment
canonical Event projection and bounded delivery
remote cancellation
readiness and drain
gRPC server and Go client
```

None of these is required when an application embeds Core directly.

## 2. Dependency direction

```text
Infrastructure → Transport goroutines → Orchestration goroutines → Agent goroutines
                                                        ├── Model contract
                                                        └── Tool/ToolSet contracts
```

The Orchestrator invokes Core APIs and never duplicates its state machine. Generated transport types do not appear in Core signatures.

## 3. Service contract

```proto
service AgentService {
  rpc Run(stream RunCommand) returns (stream RunEvent);
}
```

`Run` is one bidirectional stream attached to one Core Run. Commands are `Start`, `Steer`, `FollowUp`, and `Cancel`. Events are lifecycle, Message, Tool, ToolSet, Routine, and terminal projections.

## 4. Wire rules

The first contract is conceptually:

```proto
message RunCommand {
  oneof command {
    Start start = 1;
    Steer steer = 2;
    FollowUp follow_up = 3;
    Cancel cancel = 4;
  }
}

message Start {
  string agent_name = 1;
  string conversation_key = 2;
  oneof input {
    Message prompt = 3;
    ContinueInput continue_input = 4;
  }
  map<string, string> metadata = 5;
}
message ContinueInput {}
message Steer { Message message = 1; }
message FollowUp { Message message = 1; }
message Cancel { string reason = 1; }

message RunEvent {
  string run_id = 1;
  uint64 sequence = 2;
  EventClass event_class = 3;
  string agent_id = 4;
  string spawn_id = 5;
  string origin_run_id = 6;
  oneof event {
    AgentStart agent_start = 10;
    TurnStart turn_start = 11;
    MessageStart message_start = 12;
    MessageUpdate message_update = 13;
    MessageEnd message_end = 14;
    ToolExecutionStart tool_execution_start = 15;
    ToolExecutionUpdate tool_execution_update = 16;
    ToolExecutionEnd tool_execution_end = 17;
    ToolsetActivated toolset_activated = 18;
    RoutineStarted routine_started = 19;
    RoutineCompleted routine_completed = 20;
    RoutineFailed routine_failed = 21;
    RoutineCancelled routine_cancelled = 22;
    TurnEnd turn_end = 23;
    AgentEnd agent_end = 24;
  }
}
```

Payload messages and enums must preserve Core identity, class, correlation, and settled meaning. Contract evolution keeps field numbers stable, reserves removed fields, uses additive fields and explicit unspecified enum values, and treats unknown additive fields as ignorable.

## 5. Command state machine

```text
BeforeStart ──valid Start──► Active
BeforeStart ──other command► protocol error
Active ──────terminal──────► Terminal
Active ──────stream close──► Closed
Terminal ────delivery done──► Closed
```

`Start` is first and contains exactly one of Prompt or Continue. Duplicate Start and commands after terminal settlement are protocol errors. Cancel is idempotent only while Active; repeated Cancel after terminal settlement is a protocol error.

The command receiver serializes commands in arrival order. Runtime execution and Event sending may proceed concurrently. Acceptance does not imply immediate execution effect.

## 6. Host concurrency

The Host must support multiple streams under explicit bounds:

```text
max active streams
max active Agent goroutines
max queued requests
max dispatched Runs
per-Agent dispatch policy
Event bridge capacity
```

An Agent goroutine processes one Prompt or Continue at a time. When it is Busy, the Host policy may reject the request, queue it, prioritize it, send Steer, or send Abort. Different Agent goroutines may execute concurrently. Exact preset values are Host configuration, not Core semantics.

Admission reserves capacity before Agent construction or request dispatch and releases it exactly once.

## 7. Conversation routing in the PoC

The initial PoC assumes one Host process in one Pod:

```text
agent_name + conversation_key
              ↓
process-local routing table
              ↓
Agent handle / command channel
```

The registry and cache retain Agent handles and route requests; they do not make an Agent the owner of a Conversation or of Host resources. The Host chooses whether a request waits for the routine to become Free, is rejected, or enters a queue. The PoC makes no cross-Pod continuity claim.

### Reserved: Multi-Pod Conversation Routing

Multi-Pod continuity is outside the initial scope and remains a separate future contract. A future Host may use keyed routing, distributed ownership, or durable state restoration. Ordinary Kubernetes load balancing alone is insufficient.

## 8. Event delivery

```text
Core Event → project/redact → bounded queue → gRPC sender
```

Protected Events preserve canonical order or fail the stream. Coalescable progress may be merged. Queue-full and shutdown behavior are explicit. The sender belongs to the stream Context and cannot outlive it.

## 9. Cancellation

```text
Cancel command / stream Context / deadline / drain
                         ↓
                  Host Run Context
                         ↓
                    Core Abort
```

Stream closure ends delivery; the default attached-Run policy also cancels execution. The policy must be configurable/documented. Cancellation reaches the current Agent's Model, Tools, Extensions, observers, and local work. Cancellation of another Agent routine requires an explicit command or Host policy.

## 10. Error mapping

```text
invalid input       → InvalidArgument
unknown Agent       → NotFound
busy/invalid state  → FailedPrecondition
admission/delivery  → ResourceExhausted
cancelled           → Canceled
deadline            → DeadlineExceeded
Model unavailable   → Unavailable
internal invariant  → Internal
```

Tool failures remain Core Results and Events while the current Agent can continue. Failure in another Agent routine is delivered as a Result/Event and does not automatically terminate this Agent.

## 11. Lifecycle

The Host exposes liveness, readiness, and drain. Drain disables admission, settles or cancels active Runs, flushes or abandons Event delivery within a deadline, and then releases resources.

## 12. Infrastructure relationship

```text
Client → optional Gateway/LB → Kubernetes Service → Host Pod
```

Gateway and K8s provide network routing and process hosting. They are not required Gotato components and must not retry an active stream in a way that duplicates Start. An existing Go service may mount this Host and gRPC adapter beside its own APIs.
