# 09. Orchestration, Host, and Protocol Adapters

**Status:** Draft

> Orchestration makes Agent Cores addressable and coordinated; Host and protocol adapters expose that system through a service boundary.

## 1. Scope

The single-Agent Core path is direct. A multi-Agent system requires Orchestration to create, retain, route, schedule, and observe Agent executions through the Core contract. A Hosted deployment adds Host and a protocol adapter around that Orchestration; it does not define another Agent runtime or require a new platform.

```text
Embedded, single: Go service → Agent Core
Embedded, multi:  Go service → Orchestration → Agent Core × N
Hosted:           Client → protocol adapter → Host → Orchestration → Agent Core × N
```

A Hosted deployment may run in the same process as Orchestration and Core or across separate processes. Infrastructure remains external.

## 2. Orchestration and Host responsibilities

Orchestration MUST provide the coordination needed by a managed multi-Agent system, equivalent to:

```text
Agent definition registry and factory
Conversation identity and handle retention
Agent routing and rehydration
request admission and queue policy
per-Agent dispatch and multi-Agent coordination
Event observation and bounded delivery
Agent retirement, cancellation, and lifecycle
```

A Host MAY wrap Orchestration with:

```text
remote command and Event access
protocol stream attachment
readiness and drain
remote Agent close and retirement commands
```

Orchestration and Host MUST NOT mutate Core state or reproduce the Agent Loop. They coordinate through the Agent contract.

## 3. Protocol adapter

A protocol adapter maps a wire representation to the Host's semantic interface:

```text
wire command → semantic Agent command
canonical Event → wire Event
```

The adapter owns encoding, decoding, stream lifetime, and protocol errors. It is optional for Embedded use and is not a Core dependency.

One possible adapter is a bidirectional gRPC stream:

```proto
service AgentService {
  rpc Run(stream RunCommand) returns (stream RunEvent);
}
```

HTTP, SSE, an existing RPC system, or an in-process Go call may implement the same semantic boundary.

## 4. Wire contract example

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
  oneof conversation {
    string conversation_id = 2;
    string conversation_key = 6;
  }
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
  string conversation_id = 7;
  uint64 agent_generation = 8;
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

A wire contract MUST preserve Core identity, Event class, correlation, ordering, and settled meaning. A Hosted Start resolves either an existing ConversationID or a caller-scoped ConversationKey; the resulting stable ConversationID SHOULD be returned on RunEvents. Agent close/retirement is a separate Host lifecycle operation, not a RunCommand or stream-close side effect. A close acknowledgement means Core closure; delivery of that acknowledgement may settle later. Protobuf types MUST NOT enter Core signatures.

## 5. Command and Agent lifecycle

The protocol stream lifecycle is distinct from the Agent lifecycle:

```text
BeforeStart ── valid Start ──► Active
BeforeStart ── other command ► protocol error
Active ────── terminal ──────► Terminal
Active ────── stream close ──► Closed delivery stream
```

`Start` is first and contains exactly one Prompt or Continue. Duplicate Start and commands after terminal settlement are protocol errors. Cancel is idempotent while Active; the adapter documents behavior after terminal settlement. Closing a delivery stream does not automatically close the Agent or Conversation unless the Host policy explicitly requests Run cancellation or Agent retirement.

The command receiver serializes commands in arrival order. Runtime execution, Agent lifecycle, and Event sending may proceed concurrently. Acceptance of a command does not imply immediate execution effect.

## 6. Orchestration and Host concurrency

Orchestration and Host MUST make their bounds explicit:

```text
active streams
Agent instances
queued requests
active dispatched Runs
per-Agent dispatch policy
Event delivery bridges
```

An Agent processes one Prompt or Continue at a time. When it is Busy, Orchestration policy may reject, queue, prioritize, Steer, or Abort. Different Agents may execute concurrently. Exact values are Orchestration/Host configuration, not Core semantics.

Admission reserves capacity before Agent construction or dispatch and releases it exactly once. Retirement reserves the Conversation transition, stops new admission, persists retained state before removing the live route, and releases the Agent capacity exactly once.

## 7. Conversation routing and retirement

For a single-process Host, a routing table MAY map an application key to a Conversation record and live Agent handle:

```text
agent name + conversation key
              ↓
Conversation record / Host routing table
              ↓
live Agent handle, or Agent definition + Core snapshot
```

This is Host or application state, not Agent ownership. A retained Conversation may become Dormant after its live Agent is retired and may later be rehydrated with a new AgentID. Without a retained handle or a recoverable Conversation record, an AgentID cannot recover an in-memory Agent. Cross-process and multi-Pod continuity require the routing and persistence contract defined in [spec 16](16-agent-lifecycle-and-retirement.md).

## 8. Agent closure and Event delivery

A Host MUST distinguish Core Agent closure from remote delivery closure. A remote close command succeeds only after the Core side acknowledges `Closed`; delivery of that acknowledgement may complete later. The Host MUST NOT report a retained Conversation as closed merely because an attached stream ended.

```text
Agent lifecycle signal → Host projection → bounded delivery → client
Core execution settlement and Agent closure remain independent from delivery settlement
```

## 9. Event delivery

```text
Core Event → Host projection / redaction → bounded delivery → protocol adapter
```

Protected Events preserve canonical order or fail the consumer stream. Progress MAY be coalesced. The sender belongs to the stream Context and cannot outlive it.

Execution settlement belongs to Core. Remote delivery settlement belongs to the Host.

## 10. Cancellation and errors

```text
Cancel / stream Context / deadline / drain
                    ↓
              Host policy
                    ↓
                 Core Abort
```

Cancellation reaches the current Agent's Model, Tools, Extensions, observers, and local work. A stream close MAY cancel the attached Run, but the Host MUST document the policy.

Tool failures remain Core Results while the Agent can continue. Host admission, protocol, and delivery failures remain Host outcomes.

## 11. Internal process boundary

When Orchestration and Core share a process, a direct Go interface or channel-backed handle is the simplest connection:

```text
Orchestration → Agent interface → Core × N
```

When they have a deliberate process boundary, an internal gRPC adapter is reasonable:

```text
Orchestration service → gRPC adapter → Agent Core service(s)
```

Host and protocol adapters may sit in front of Orchestration, but the Core contract remains unchanged.

Both forms use the same semantic contract. The Agent Core does not depend on gRPC.

## 12. Infrastructure relationship

```text
Existing Gateway / LB / Kubernetes / Go service
                         ↓
                    Gotato Host
```

Infrastructure is outside this specification. It only needs to satisfy the Host's integration requirements, such as Context propagation, long-lived stream support, no duplicate retry of an active Run, and readiness/drain signals.
