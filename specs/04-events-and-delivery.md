# 04. Events and Delivery

**Status:** Draft

> Agent goroutines emit immutable facts. Channels carry those facts to local observers and Hosted delivery.

## 1. Event kinds

Core MUST expose semantic equivalents of:

```text
agent_start, turn_start
message_start, message_update, message_end
tool_execution_start, tool_execution_update, tool_execution_end
toolset_activated
routine_started, routine_completed, routine_failed, routine_cancelled
turn_end, agent_end
```

Routine lifecycle Events describe an Agent Routine or spawn operation. They MUST NOT imply parent/child resource ownership.

## 2. Event classes

Every Event belongs to exactly one class:

```text
Protected: lifecycle transitions and settled outcomes
           agent/turn lifecycle, Message start/end, Tool start/end,
           activation, Routine terminal Events, turn_end, agent_end

Coalescable: optional progress
             Message updates, Tool updates, Routine progress
```

A Protected Event must reach a consumer in canonical order or that consumer's delivery fails. Coalescable progress may be merged or omitted and must not contain authoritative information absent from its settling Protected Event.

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

## 5. Production

The Agent goroutine creates an Event only for a committed transition or a declared operation start. It never retracts an Event. No Event is created after `agent_end`. Publication occurs through the Agent Event boundary in production order.

## 6. Local subscribers

A Core subscriber is an in-process, Context-aware, bounded observer receiving Events from the Agent Event channel. Core MAY await a blocking observer at its declared boundary; an advisory observer MUST NOT block the Agent Loop. Panics are recovered according to the Extension contract.

A subscriber cannot be a remote network peer.

## 7. Host delivery bridge

A Host that delivers Events remotely MUST use a bounded bridge:

```text
Agent Event → projection/redaction → bounded queue → sender → client
```

The bridge MUST declare capacity, Protected kinds, coalescing, queue-full behavior, and shutdown flush deadline. Enqueue and sender operations must honor the Context that owns them. Detached senders and unbounded queues are forbidden defaults.

Protected Events take priority over optional progress. If a Protected Event cannot be preserved within policy, the Host fails the consumer stream rather than silently dropping it.

## 8. Settlement

```text
Agent execution settlement   current Run and local work are complete
Host delivery settlement     remote delivery is drained or abandoned
```

`WaitForIdle` observes Agent execution settlement only. Queue settlement and remote delivery settlement belong to the caller or Host.

## 9. Cancellation and disconnect

Disconnect ends delivery. Host policy decides whether it also cancels the current Agent Run. Explicit Cancel, deadlines, and drain send cancellation through the Agent control boundary and reach its Model, Tools, observers, and local work.

Cancellation of another Agent Routine requires an explicit command or selected policy.

## 10. Projection

A projection may filter, redact, enrich, and encode a Core Event. It must preserve Event class, identity, correlation, and settled meaning. `RunEvent` is not a second event history.

## 11. Acceptance

Tests MUST prove Event order, classification, correlation, one terminal Event per Run, Protected Event delivery, progress coalescing, bounded channel behavior, observer bounds, independent Agent/delivery settlement, disconnect behavior, and bounded drain.
