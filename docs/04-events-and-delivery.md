# Events and Delivery

**Status:** Draft

> Core emits Agent facts. A Host may observe and deliver them.

## 1. Two uses of an Event

```text
Agent Core
  └── canonical immutable Events
          ├── local observation
          └── Host delivery → remote client
```

Core creates Events for Agent state transitions and declared operations. A Host may project those Events for a protocol or client. The projection is delivery, not a second Event history.

## 2. Event classes

Every Event belongs to one class:

```text
Protected
  lifecycle transitions and settled outcomes

Coalescable
  optional streamed progress
```

Protected Events must remain ordered and reach a consumer or cause that delivery to fail. Coalescable progress may be merged, thinned, or omitted. It must not contain authoritative information absent from its settling Protected Event.

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

`Sequence` starts at 1 per Run, increases strictly, and is assigned during the Core state transition before publication. Timestamp is diagnostic. Correlation fields that do not apply to an Event kind are empty.

## 4. Ordering

A normal Run orders Events as:

```text
agent_start
  turn_start
  input Message lifecycle when applicable
  assistant Message lifecycle
  Tool execution and Tool Result lifecycle
  turn_end
  ...
agent_end
```

Parallel Tool completion Events reflect actual completion order; transcript commitment remains source ordered. Independent Agents have independent sequences. Spawn provenance does not merge histories.

## 5. Local observation

Core provides a local Event boundary for in-process observers:

```text
Agent Core → Event observer
```

An observer is Context-aware and bounded. A blocking observer may hold Core at its declared boundary; an advisory observer must not block the Loop. An observer cannot wait on a remote network peer or an unbounded queue.

## 6. Hosted delivery

A Host may deliver Core Events to a remote client:

```text
Core Event
   ↓ Host projection / redaction
bounded delivery bridge
   ↓ protocol adapter
remote client
```

The Host declares capacity, Protected Event handling, coalescing, queue-full behavior, and shutdown deadline. It must not silently drop a Protected Event or grow memory without a bound.

A protocol adapter only maps the Host's semantic Events to a wire representation. It does not define Core Event meaning.

## 7. Two settlements

```text
Agent settlement    current Run and local work are complete
Host settlement      remote delivery is drained or abandoned
```

Core does not wait for remote delivery before returning its result. A client may disconnect while the Agent continues, depending on Host policy.

## 8. Cancellation and disconnect

A disconnected client ends delivery. The Host documents whether it also cancels the attached Run. Explicit Cancel, deadlines, and drain send cancellation through the Agent boundary and reach its Model, Tools, observers, and local work.

Cancellation of another Agent requires an explicit command or selected Host/application policy.

## 9. Spawned Agents

A spawned Agent has its own Event channel and sequence. A Host may project selected Events onto an origin stream using explicit correlation:

```text
origin AgentID / RunID
spawned AgentID / RunID
SpawnID
```

The projection must not pretend that independent Agents share one transcript or one Event sequence.

## 10. Drain

```text
stop new admission
  ↓
queued requests handled by Host policy
  ↓
active Runs settle or cancel
  ↓
delivery bridges flush within deadline
  ↓
remaining delivery abandoned
```

Abandoning delivery after execution settlement loses transmission, not Core history.

## 11. Acceptance

Tests prove canonical order, classification, correlation, one terminal Event per Run, bounded observation and delivery, Protected Event handling, progress coalescing, independent execution/delivery settlement, disconnect behavior, and bounded drain.
