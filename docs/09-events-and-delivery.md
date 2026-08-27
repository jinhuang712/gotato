# Events and Delivery

**Status:** Draft

> A Run produces facts. Consumers receive projections of those facts under an explicit bound.

## 1. Role

Events are how anything outside the Model/Tool loop learns what happened. A gRPC client, a log pipeline, a trace exporter, and a test recorder all read the same runtime history.

```text
Runtime transition
        ↓
Canonical Event
        ├──► gRPC client
        ├──► observer
        ├──► telemetry
        └──► test recorder
```

An Event is an immutable fact. Consumers choose their representation of it; they do not change what happened.

## 2. Kinds

```text
agent_start              the Run was accepted
turn_start / turn_end    one Model response and its Tool batch
message_start            a Message begins
message_update           streamed delta for the Message under construction
message_end              the Message is committed to the transcript
tool_execution_start     a validated Tool Use begins
tool_execution_update    bounded progress from a running Tool
tool_execution_end       a settled Tool outcome
toolset_activated        capability visibility changed
routine_started          a child Agent Run began
routine_completed        a child Agent Run settled successfully
routine_failed           a child Agent Run returned a terminal error
routine_cancelled        a child Agent Run was cancelled
agent_end                the Run is over
```

## 3. Two classes

Not every Event carries the same obligation.

```text
Protected                       Coalescable
─────────                       ───────────
agent_start                     message_update
turn_start · turn_end           tool_execution_update
message_start · message_end     Routine progress
tool_execution_start · _end
toolset_activated
routine_* terminal Events
agent_end
```

A protected Event is a lifecycle transition or a settled outcome. Every consumer receives each one, in canonical order, or its stream fails.

A coalescable Event is optional progress. A consumer under load may merge several into one, keep only the newest, or skip them entirely. Progress exists so a human sees motion; it never carries a fact that the corresponding settled Event does not also carry.

This split is what makes bounded delivery possible without silently losing history.

## 4. Ordering

Order within a Run is canonical:

```text
agent_start
  turn_start
    user Message lifecycle when a Prompt supplied input
    assistant message_start
    assistant message_update ...
    assistant message_end
    Tool execution Events
    Tool Result Message lifecycle
  turn_end
  ...
agent_end
```

Parallel Tool batches complete out of order, and their completion Events say so. Transcript commitment stays in assistant source order, so what the Model sees next is deterministic even when what the client saw was not:

```text
Completion Events   C → A → B     actual
Transcript          A → B → C     deterministic
```

Correlation travels with every Event: Run ID, Turn sequence, Tool Call ID, and for child work the Routine ID together with both parent and child Run IDs.

## 5. One terminal Event

`agent_end` is final. A Run has exactly one, and nothing resumes execution after it.

This is a deliberate constraint on where orchestration lives. Automatic retry after a transient Model failure, context compaction when the transcript outgrows the window, and continuation for queued Steering or Follow-up all happen inside the Run:

```text
Model failure
      ↓
retry inside the Run
      ↓
      ...
      ↓
agent_end        ← the only completion signal
```

The alternative is an orchestration layer above the loop that calls the Runtime again after it finished. That design forces every client to learn that the first completion signal is not the real one, and a cross-language gRPC contract cannot carry that footnote safely.

## 6. Observers hold the loop

An observer runs inside the Run and is awaited before the loop proceeds.

```text
Canonical Event
      ↓
observer runs
      ↓
loop continues
```

For an in-process consumer this is a feature: exact ordering, no buffering, and natural pacing without extra machinery. A test recorder or a metrics counter wants precisely this.

It is also a privilege, so the Runtime bounds who may claim it:

```text
An observer is in-process, fast, and Context-aware.
An observer does not block on a network peer, on a remote lock,
or on any wait that has no bound of its own.
```

A remote consumer never attaches here.

## 7. Backpressure

Backpressure is what a system does when a producer outruns a consumer.

A Model can stream hundreds of deltas per second. A client on a poor connection may accept a handful. The gap has only three possible answers:

```text
buffer without limit   memory grows until the process dies
discard silently       the consumer's history is wrong and cannot tell
slow the producer      the producer pays for the consumer's speed
```

Every streaming system picks among these three. The failure mode is picking by accident: an unbounded queue is a choice, just an unexamined one.

Gotato picks explicitly, and picks differently on each side of the delivery boundary.

## 8. The bounded bridge

Remote delivery crosses a queue with a stated capacity:

```text
Runtime goroutine                Sender goroutine
─────────────────                ────────────────
emit → observers → enqueue       dequeue → grpc.Send
awaited · local · fast           slow · remote · bounded
```

The bridge declares:

```text
capacity
which Events are protected
how coalescing merges progress
what happens when the queue is full
how in-flight Events settle at shutdown
```

## 9. When the queue fills

One documented policy applies:

```text
block        slow the producer within an explicit bound
coalesce     merge pending progress and keep the newest
terminate    fail the stream with a resource-exhausted error
```

Blocking is bounded and Context-aware. An unbounded wait would hand one slow client the ability to hold a Run open indefinitely, which is the outcome the bridge exists to prevent.

Coalescing applies only to the coalescable class. Terminating is preferable to dropping a protected Event: a client whose stream fails knows it lost something, while a client silently missing `tool_execution_end` does not.

## 10. Two settlements

Two different moments are called settlement.

```text
Execution settlement
  the Run is over and owns no further work
  Model stream closed · Tools finished
  Routines settled · observers returned
  owned by the Runtime

Delivery settlement
  the consumer has received everything it will receive
  queue drained or abandoned · stream closed
  owned by the Service
```

They are independent in both directions. A client that disconnects mid-Run ends delivery while execution continues to its own terminal Event. A Run that finishes in milliseconds may still have Events in flight for seconds.

Conflating them produces two distinct bugs: a Runtime that cannot finish because a socket is slow, and a service that reports success before the client has anything.

## 11. Cancellation and disconnect

A disconnect is not a cancellation. It ends delivery; it says nothing about intent.

```text
explicit Cancel   → cancel the Run Context
stream closed     → service policy decides
Run deadline      → cancel the Run Context
drain deadline    → cancel the Run Context
```

When the service treats a closed stream as cancellation, which is the usual choice for an attached Run, it cancels the Run Context. Cancellation then reaches the Model, the Tools, the observers, and every Agent Routine through one mechanism.

## 12. Shutdown

Drain stops new admission and lets active Runs reach execution settlement within a deadline. In-flight Events then get a bounded window to reach their consumers.

```text
drain requested
      ↓
no new Runs admitted
      ↓
active Runs reach execution settlement or are cancelled
      ↓
bridges flush within the delivery deadline
      ↓
process exits
```

A bridge that cannot flush in time abandons delivery rather than delaying shutdown. The execution history is already complete; only its transmission was lost.

## 13. Testing

Event behavior is testable without a network:

```text
canonical order matches state transitions
protected Events survive coalescing under load
agent_end is the last Event of every Run
an observer that blocks past its bound is detected
a full queue applies its documented policy
a disconnected client does not stall an unrelated Run
drain flushes or abandons within its deadline
```

## 14. Ownership

```text
Runtime
  Event kind · production point · order · correlation
  terminal Event · execution settlement · observer contract

Service
  projection · redaction · queue capacity
  coalescing · slow-consumer policy · delivery settlement

Transport
  wire encoding · stream lifetime · status mapping

Application
  what it does with the facts
```
