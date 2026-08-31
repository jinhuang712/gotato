# Events and Delivery

**Status:** Draft

> **Agents emit facts. Hosts deliver them.**

## 1. Two layers

```text
Agent goroutine
  └── canonical immutable Events
          ├── local Event channel
          └── Host projection → bounded delivery → remote client
```

The Agent goroutine produces Event kind, production point, sequence, correlation, and terminal settlement. Host and Transport project, redact, buffer, and deliver those Events.

## 2. Event classes

Every Event belongs to one class:

```text
Protected
  lifecycle transitions and settled outcomes
  agent_start, turn_start, message_start/end
  tool start/end, ToolSet activation
  Agent Routine lifecycle, turn_end, agent_end

Coalescable
  optional streamed progress
  message_update, tool_execution_update, Routine progress
```

Protected Events must be delivered in canonical order or the consumer stream fails. Coalescable progress may be merged, thinned, or omitted. Progress must not contain authoritative information absent from its settling protected Event.

## 3. Canonical shape

```go
type Event struct {
    AgentID     AgentID
    RunID       RunID
    Sequence    uint64
    Kind        EventKind
    Class       EventClass
    Turn        TurnNumber
    MessageID   MessageID
    ToolCallID  ToolCallID
    SpawnID     SpawnID
    OriginRunID RunID
    Payload     EventPayload
    Timestamp   time.Time
}
```

`Sequence` starts at 1 per Run, increases strictly, and is assigned during the Agent state transition before publication. Timestamp is diagnostic only. Correlation fields not applicable to a kind are empty.

## 4. Ordering

A normal Run orders Events as:

```text
agent_start
  turn_start
  Prompt user Message lifecycle when applicable
  assistant Message lifecycle
  Tool execution and Tool Result lifecycle
  turn_end
  ...
agent_end
```

Parallel Tool completion Events reflect actual completion order; transcript commitment remains source ordered. Each Agent Routine and Run has its own Event sequence. Spawn provenance does not merge Event histories.

## 5. Local observation

The Agent publishes Events through a local channel-backed subscription boundary:

```text
Agent goroutine → Event channel → observer
```

An observer is local, Context-aware, and bounded. A blocking observer may hold the Agent at its declared boundary; an advisory observer must not block the Agent Loop. An observer cannot wait on a network peer, unbounded queue, or remote lock.

A remote client is not an Agent observer. It receives a Host projection.

## 6. Host delivery

A Host bridges Agent Events to a remote transport:

```text
Agent Event
   ↓ channel
projection / redaction
   ↓
bounded per-consumer bridge
   ↓
sender goroutine
   ↓
remote client
```

The bridge declares:

```text
capacity
protected Event set
coalescing behavior
queue-full policy
shutdown flush deadline
```

The bridge and sender are owned by the stream that created them. No sender goroutine or queue may outlive that stream's Context.

## 7. Backpressure

When a consumer is slower than the producer, the Host must explicitly choose:

```text
bounded blocking
coalescing optional progress
stream termination
```

It must never silently drop a Protected Event. If it cannot preserve one within its bound, it fails the consumer stream. A slow client cannot hold an unrelated Agent goroutine open or grow memory without limit.

## 8. Two settlements

```text
Agent execution settlement   current Run and local owned work are complete
Host delivery settlement     remote Event delivery is drained or abandoned
```

A client may disconnect while the Agent continues to its terminal Event. A fast Run may settle while delivery is still in flight. `WaitForIdle` observes Agent execution settlement only. Queue settlement and remote delivery are Host concerns.

## 9. Cancellation

A disconnected stream ends delivery. Whether it also cancels the current Agent Run is a Host policy. Explicit Cancel, Run deadlines, and drain deadlines send cancellation through the Agent control boundary and reach its Model, Tools, observers, and local work.

Cancellation of another Agent Routine requires an explicit command or application/Host policy.

## 10. Spawned Agents

A spawned Agent has its own Event channel and sequence. A Host may project selected Events onto an origin stream using explicit correlation:

```text
origin AgentID / RunID
spawned AgentID / RunID
SpawnID
```

The projection must not pretend that independent Agents share one transcript or one Event sequence.

## 11. Drain

During drain:

```text
stop new admission
  ↓
queued requests handled by Host policy
  ↓
active Agent Runs settle or are cancelled
  ↓
bridges flush within deadline
  ↓
abandon remaining delivery
```

Abandoning delivery after execution settlement loses transmission, not Agent history.

## 12. Acceptance

Tests must prove canonical order, one terminal Event per Run, class preservation, channel bounds, Protected Event handling, slow-consumer behavior, observer bounds, independent execution/delivery settlement, disconnect behavior, and bounded drain.
