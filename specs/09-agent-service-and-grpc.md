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

Contract evolution MUST follow standard Protobuf compatibility rules:

```text
field numbers remain stable
removed fields are reserved
new fields are additive
enums retain an unspecified value
oneof command and Event variants evolve compatibly
```

The build SHOULD compare each change against the previously released contract so a breaking change fails before release.

## 4. Wire schema

The first-party contract is conceptually equivalent to this Protobuf schema. Names and package paths MAY change before the first compatibility commitment, but field meaning, oneof exclusivity, and lifecycle behavior MUST remain explicit:

```proto
syntax = "proto3";

package gotato.agent.v1;

service AgentService {
  rpc Run(stream RunCommand) returns (stream RunEvent);
}

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

message Steer {
  Message message = 1;
}

message FollowUp {
  Message message = 1;
}

message Cancel {
  string reason = 1;
}

message RunEvent {
  string run_id = 1;
  uint64 sequence = 2;
  EventClass event_class = 3;
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

enum EventClass {
  EVENT_CLASS_UNSPECIFIED = 0;
  EVENT_CLASS_PROTECTED = 1;
  EVENT_CLASS_COALESCABLE = 2;
}

enum Role {
  ROLE_UNSPECIFIED = 0;
  ROLE_USER = 1;
  ROLE_ASSISTANT = 2;
  ROLE_TOOL_RESULT = 3;
}

enum StopReason {
  STOP_REASON_UNSPECIFIED = 0;
  STOP_REASON_NONE = 1;
  STOP_REASON_END_TURN = 2;
  STOP_REASON_TOOL_CALLS = 3;
  STOP_REASON_MAX_TOKENS = 4;
  STOP_REASON_CANCELLED = 5;
  STOP_REASON_ERROR = 6;
}

enum RunStatus {
  RUN_STATUS_UNSPECIFIED = 0;
  RUN_STATUS_COMPLETED = 1;
  RUN_STATUS_FAILED = 2;
  RUN_STATUS_CANCELLED = 3;
  RUN_STATUS_DEADLINE_EXCEEDED = 4;
  RUN_STATUS_LIMIT_EXCEEDED = 5;
}

enum ToolResultStatus {
  TOOL_RESULT_STATUS_UNSPECIFIED = 0;
  TOOL_RESULT_SUCCEEDED = 1;
  TOOL_RESULT_FAILED = 2;
  TOOL_RESULT_BLOCKED = 3;
  TOOL_RESULT_CANCELLED = 4;
}

message Message {
  string id = 1;
  Role role = 2;
  repeated ContentPart parts = 3;
  repeated ToolCall tool_calls = 4;
  ToolResult tool_result = 5;
  Usage usage = 6;
  StopReason stop_reason = 7;
}

message ContentPart {
  string kind = 1;
  string text = 2;
  bytes data = 3;
  string mime_type = 4;
  map<string, string> metadata = 5;
}

message ToolCall {
  string id = 1;
  string tool_id = 2;
  bytes arguments_json = 3;
}

message ToolResult {
  string call_id = 1;
  ToolResultStatus status = 2;
  repeated ContentPart content = 3;
  map<string, string> metadata = 4;
  string safe_error = 5;
  bool executed = 6;
}

message Usage {
  uint64 input_tokens = 1;
  uint64 output_tokens = 2;
  uint64 cached_input_tokens = 3;
  uint64 reasoning_tokens = 4;
}

message AgentStart {
  string agent_name = 1;
}

message TurnStart {
  uint32 turn = 1;
}

message TurnEnd {
  uint32 turn = 1;
  bool continuation_requested = 2;
}

message MessageStart {
  string message_id = 1;
  Role role = 2;
}

message MessageUpdate {
  string message_id = 1;
  ContentPart delta = 2;
}

message MessageEnd {
  string message_id = 1;
  Message message = 2;
}

message ToolExecutionStart {
  string tool_call_id = 1;
  string qualified_tool_id = 2;
  uint32 turn = 3;
}

message ToolExecutionUpdate {
  string tool_call_id = 1;
  string text = 2;
  map<string, string> metadata = 3;
}

message ToolExecutionEnd {
  string tool_call_id = 1;
  ToolResult result = 2;
}

message ToolsetActivated {
  string name = 1;
  repeated string visible_tool_ids = 2;
}

message RoutineStarted {
  string routine_id = 1;
  string routine_name = 2;
  string parent_run_id = 3;
  string child_run_id = 4;
}

message RoutineCompleted {
  RoutineResult result = 1;
}

message RoutineFailed {
  RoutineResult result = 1;
}

message RoutineCancelled {
  RoutineResult result = 1;
}

message RoutineResult {
  string routine_id = 1;
  RunStatus child_status = 2;
  Message final_message = 3;
  Usage usage = 4;
  ErrorInfo error = 5;
}

message AgentEnd {
  RunStatus status = 1;
  Message final_message = 2;
  Usage usage = 3;
  ErrorInfo error = 4;
}

message ErrorInfo {
  string code = 1;
  string message = 2;
  map<string, string> details = 3;
}
```

A `RunCommand` contains exactly one command variant. A `Start` contains exactly one of `prompt` and `continue_input`; an empty or ambiguous start is invalid. A `RunEvent` contains exactly one Event variant and the envelope sequence is authoritative for ordering.

The wire schema MUST use an explicit unspecified enum value, MUST keep removed field numbers reserved, and MUST treat unknown additive fields as ignorable by older clients. A client MUST NOT infer terminality from a missing optional payload; only `agent_end` is terminal.

## 5. Command protocol

```text
Start     → Prompt or Continue
Steer     → Steering queue
FollowUp  → Follow-up queue
Cancel    → idempotent cancellation while the Run is active
```

- `Start` MUST be the first command.
- One stream MUST own one attached Run.
- A duplicate `Start` MUST produce a protocol error.
- Steering and Follow-up MUST preserve accepted order.
- `Cancel` MUST be idempotent.
- The terminal Run Event MUST close command acceptance.
- A command after terminal settlement MUST produce a protocol error.

Command acceptance is distinct from execution effect. The service acknowledges that a command was accepted; runtime rules determine when an accepted Steering or Follow-up Message affects a Turn. An acknowledgement MUST NOT imply completion.

## 6. Attached stream state

The server-side stream has four protocol states:

```text
BeforeStart ──valid Start──► Active
BeforeStart ──other command► protocol error
Active ──────terminal──────► Terminal
Active ──────stream close──► Closed
Terminal ────delivery done──► Closed
```

The command receiver MUST serialize commands in arrival order. The Runtime Run and Event sender MAY execute concurrently, but only the receiver accepts commands and only the Runtime decides their execution effect.

In `BeforeStart`, the server MUST reject `Steer`, `FollowUp`, and `Cancel`. In `Active`, it MUST reject a second `Start`. In `Terminal`, it MUST reject every command, including repeated `Cancel`, because the terminal Event has closed command acceptance. `Cancel` is idempotent only while the attached Run is active; a repeated cancel after terminal settlement is a protocol error, not a second outcome.

The server MUST send the projected terminal `agent_end` before delivery settlement when the bridge can deliver it. It MUST close the stream after the queue is drained, abandoned by policy, or cancelled by the peer. Stream closure MUST release the Agent cache lease and all sender resources.

## 7. Agent factory

```go
type AgentFactory interface {
    NewAgent(context.Context, AgentRequest) (*gotato.Agent, error)
}
```

Final naming MAY evolve. The contract MUST create or restore isolated Agent state for a service request.

The preset MUST support deterministic registration of named factories. Factory construction MUST make Models, Tools, ToolSets, Extensions, and limits explicit.

## 8. Conversation resolution

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

## 9. Agent cache

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

The draft cache boundary is:

```go
type AgentCache interface {
    Acquire(context.Context, ConversationKey) (AgentLease, error)
    Reset(context.Context, ConversationKey) error
}

type AgentLease interface {
    Agent() *Agent
    Release() error
}
```

`Acquire` serializes creation for one key. A lease pins the entry until `Release`; `Release` is idempotent. Eviction may inspect only entries with no active leases and no active Run. Reset rejects or waits according to an explicit policy when the entry is pinned; it MUST NOT silently discard a live Agent.

## 10. Event delivery

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

## 11. Cancellation

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

## 12. Admission

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

A service MAY expose the following boundary:

```go
type AdmissionController interface {
    Admit(context.Context, AgentRequest) (AdmissionLease, error)
}

type AdmissionLease interface {
    Release()
}
```

Admission MUST reserve capacity before Agent construction or cache pinning. A rejected request MUST release no partially acquired Agent lease because none may have been created for that request. A successful admission lease remains held until the attached Run has reached the service's ownership handoff or terminal rejection.

## 13. Error mapping

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

## 14. Lifecycle signals

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

## 15. Deployment baseline

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

## 16. Optional projections

An HTTP/SSE or Connect-style adapter MAY project the same factories, cache, commands, Events, errors, and lifecycle semantics. It MUST NOT introduce alternative Agent semantics.

## 17. Acceptance

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
