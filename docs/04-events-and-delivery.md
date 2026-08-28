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

The facts a Run produces are its lifecycle transitions, the Messages it commits, the Tool Uses it executes, the capability changes it makes, the child Runs it owns, and its terminal outcome.

## 2. Two classes

Not every Event carries the same obligation.

A **protected** Event is a lifecycle transition or a settled outcome. Every consumer receives each one, in canonical order, or that consumer's stream fails.

A **coalescable** Event is optional progress. A consumer under load may merge several into one, keep only the newest, or skip them entirely.

What makes the split safe is a constraint on the producer rather than a permission for the consumer: progress exists so a human sees motion, and it never carries a fact that the Event settling the same operation does not also carry. A consumer that receives no progress at all still receives a complete history.

Without this split, bounded delivery has no honest implementation. Either every delta is guaranteed, which hands a slow client control over the Run, or facts are dropped by whichever ones happened to arrive during congestion, and no consumer can tell which.

## 3. Ordering

Order within a Run is canonical, and one Run can be true in two orders at once.

Parallel Tool batches complete out of order, and their completion Events say so. Transcript commitment stays in assistant source order. The client therefore sees what actually happened, while the next Model Turn sees a deterministic history:

```text
Completion Events   C → A → B     actual
Transcript          A → B → C     deterministic
```

Neither order is a projection of the other. Collapsing them would force a choice between lying to the client about concurrency and making Model input depend on scheduling.

Correlation travels with every Event so a consumer can place it without inferring anything from arrival order.

## 4. One terminal Event

`agent_end` is final. A Run has exactly one, and nothing resumes execution after it.

This is a deliberate constraint on where orchestration lives. Automatic retry after a transient Model failure, context compaction when the transcript outgrows the window, and continuation for queued Steering or Follow-up all happen inside the Run, before that Event.

The alternative is an orchestration layer above the loop that calls the Runtime again after it finished. That design forces every client to learn that the first completion signal is not the real one, and a cross-language gRPC contract cannot carry that footnote safely.

## 5. Observers hold the loop

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

A remote consumer never attaches here. Being awaited means being able to stall the Model/Tool loop, and a network peer has no bound of its own to offer.

## 6. Backpressure

Backpressure is what a system does when a producer outruns a consumer.

A Model can stream hundreds of deltas per second. A client on a poor connection may accept a handful. The gap has only three possible answers:

```text
buffer without limit   memory grows until the process dies
discard silently       the consumer's history is wrong and cannot tell
slow the producer      the producer pays for the consumer's speed
```

Every streaming system picks among these three. The failure mode is picking by accident: an unbounded queue is a choice, just an unexamined one.

Gotato picks explicitly, and picks differently on each side of the delivery boundary.

## 7. The bounded bridge

Remote delivery crosses a queue with a stated capacity, which is what keeps the awaited side and the network side apart:

```text
Runtime goroutine                Sender goroutine
─────────────────                ────────────────
emit → observers → enqueue       dequeue → grpc.Send
awaited · local · fast           slow · remote · bounded
```

A bridge that does not state its capacity, its protected kinds, its coalescing behavior, its queue-full behavior, and its shutdown behavior has still chosen all five. It has just chosen them where nobody can review the choice.

When the queue fills, the answer is one of blocking the producer within an explicit bound, coalescing pending progress, or terminating the stream. Blocking is bounded and Context-aware; an unbounded wait would hand one slow client the ability to hold a Run open indefinitely, which is the outcome the bridge exists to prevent.

Terminating is preferable to dropping a protected Event. A client whose stream fails knows it lost something; a client silently missing `tool_execution_end` does not.

## 8. Two settlements

Two different moments are called settlement, and they have different owners.

```text
Execution settlement   the Run is over and owns no further work    Runtime
Delivery settlement    the consumer has all it will receive        Service
```

They are independent in both directions. A client that disconnects mid-Run ends delivery while execution continues to its own terminal Event. A Run that finishes in milliseconds may still have Events in flight for seconds.

Conflating them produces two distinct bugs: a Runtime that cannot finish because a socket is slow, and a service that reports success before the client has anything.

## 9. Cancellation and disconnect

A disconnect is not a cancellation. It ends delivery; it says nothing about intent.

Treating a closed stream as cancellation is the usual choice for an attached Run, because nobody remains who wanted the answer. It is still a choice the service makes and states, not a fact the transport reports. Every source that does mean cancellation — an explicit command, a Run deadline, a drain deadline — converges on the Run Context, and from there reaches the Model, the Tools, the observers, and every Agent Routine through one mechanism.

## 10. Shutdown

Drain stops new admission and lets active Runs reach execution settlement within a deadline. In-flight Events then get a bounded window to reach their consumers.

A bridge that cannot flush in that window abandons delivery rather than delaying shutdown. The execution history is already complete at that point; only its transmission is lost, and that is the cheaper of the two things to lose.

## 11. Ownership

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
