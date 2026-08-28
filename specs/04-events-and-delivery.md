# 04. Events and Delivery

**Status:** Draft

> Core Events are immutable facts. Hosts deliver projections of those facts under explicit bounds.

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
    RunID RunID
    Sequence uint64
    Kind EventKind
    Class EventClass
    Turn TurnNumber
    MessageID MessageID
    ToolCallID ToolCallID
    RoutineID RoutineID
    ParentRunID RunID
    ChildRunID RunID
    Payload EventPayload
    Timestamp time.Time
}
```

`Sequence` starts at 1 per Run, increases strictly, and is assigned during the state transition before observer dispatch. Timestamp is diagnostic only. Correlation fields not applicable to a kind are empty.

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

Parallel Tool completion Events reflect actual completion order; transcript commitment remains source ordered. Child Event sequence remains scoped to the child Run.

## 5. Production

Core creates an Event only for a committed transition or a declared operation start. It never retracts an Event. No Event is created after `agent_end`. Observers receive Events in production order and registration order.

## 6. Local subscribers

A Core subscriber is an in-process, Context-aware, bounded observer. Core awaits it before continuing. Blocking and advisory failure modes are explicit, and panics are recovered. A subscriber cannot be a remote network peer.

## 7. Host delivery bridge

A Host that delivers Events remotely MUST use a bounded bridge:

```text
Core Event → projection/redaction → bounded queue → sender → client
```

The bridge MUST declare capacity, Protected kinds, coalescing, queue-full behavior, and shutdown flush deadline. Enqueue and sender operations must honor the Context that owns them. Detached senders and unbounded queues are forbidden defaults.

Protected Events take priority over optional progress. If a Protected Event cannot be preserved within policy, the Host fails the consumer stream rather than silently dropping it.

## 8. Settlement

Execution settlement means Core owns no further work, including child Routines and terminal observers. Delivery settlement means Host delivery has drained or been abandoned. `WaitForIdle` observes only execution settlement.

## 9. Cancellation and disconnect

Disconnect ends delivery. Host policy decides whether it also cancels the Run; attached-Run hosting normally does. Explicit Cancel, deadlines, and drain cancel the Run Context and reach all owned operations.

## 10. Projection

A projection may filter, redact, enrich, and encode a Core Event. It must preserve Event class, identity, correlation, and settled meaning. `RunEvent` is not a second event history.

## 11. Acceptance

Tests MUST prove Event order, classification, correlation, one terminal Event, protected delivery, progress coalescing, bounded queue behavior, observer bounds, independent execution/delivery settlement, disconnect behavior, and bounded drain.
