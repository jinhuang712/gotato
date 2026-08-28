# Events and Delivery

**Status:** Draft

> Core Events describe what happened. A Host decides how those facts cross a boundary.

## 1. Two layers

```text
Agent Core
  └── canonical immutable Events
          ├── local subscriber
          └── Host projection → bounded delivery → remote client
```

Core owns Event kind, production point, sequence, correlation, and terminal settlement. Host and Transport own projection, redaction, buffering, and delivery.

## 2. Event classes

Every Event belongs to one class:

```text
Protected
  lifecycle transitions and settled outcomes
  agent_start, turn_start, message_start/end
  tool start/end, ToolSet activation
  Routine terminal Events, turn_end, agent_end

Coalescable
  optional streamed progress
  message_update, tool_execution_update, Routine progress
```

Protected Events must be delivered in canonical order or the consumer stream fails. Coalescable progress may be merged, thinned, or omitted. Progress must not contain authoritative information absent from its settling protected Event.

## 3. Core sequence

Each Run assigns strictly increasing sequence numbers when Events are created:

```text
agent_start
  turn_start
  message lifecycle
  Tool lifecycle
  tool result lifecycle
  turn_end
agent_end
```

Parallel Tool completion Events reflect actual completion order. Transcript Tool Results remain committed in assistant source order. Event sequence is not a timestamp and must not be inferred from arrival time.

## 4. Local observation

A Core subscriber is an in-process observer:

```text
create Event → observer A → observer B → loop continues
```

Observers run in registration order and are awaited. They must be Context-aware and bounded; they cannot wait on a network peer, unbounded queue, or remote lock. Blocking and advisory failure modes are explicit.

A remote client is not a Core observer.

## 5. Host delivery

A Host bridges Core Events to a remote transport:

```text
Core Event
   ↓
projection / redaction
   ↓
bounded per-consumer bridge
   ↓
sender
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

Its enqueue and sender operations belong to their respective Contexts. No sender goroutine or queue may outlive the stream that authorized it.

## 6. Backpressure

When a consumer is slower than the producer, the Host must explicitly choose:

```text
bounded blocking
coalescing optional progress
stream termination
```

It must never silently drop a Protected Event. If it cannot preserve one within its bound, it fails the consumer stream. A slow client cannot hold an unrelated Run open or grow memory without limit.

## 7. Two settlements

```text
Execution settlement   Core owns no further work
Delivery settlement    Host has drained or abandoned delivery
```

A client may disconnect while Core continues to its terminal Event. A fast Run may settle while delivery is still in flight. `WaitForIdle` observes execution settlement only.

## 8. Cancellation

A disconnected stream ends delivery. Whether it cancels execution is a Host policy; attached Runs normally cancel on stream closure. Explicit Cancel, Run deadlines, and drain deadlines cancel the Run Context and reach Model, Tools, observers, and Routines.

## 9. Child Runs

Parent-facing Routine lifecycle Events are part of the parent Run Event view. Detailed child Events retain child Run correlation and may be exposed separately. Core sequence is scoped to each Run; Host projection must not pretend that parent and child have one transcript.

## 10. Drain

During drain:

```text
stop new admission
  ↓
active Runs settle or are cancelled
  ↓
bridges flush within deadline
  ↓
abandon remaining delivery
```

Abandoning delivery after execution settlement loses transmission, not Core history.

## 11. Acceptance

Tests must prove canonical order, one terminal Event, class preservation, protected-event handling, bounded queues, slow-consumer behavior, observer bounds, independent execution/delivery settlement, and bounded drain.
