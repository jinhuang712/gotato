# 04. Events and Delivery

**Status:** Draft

> Events are the runtime's record of what happened. Delivery is a separate, bounded promise about who receives that record.

```text
Runtime transition
        ↓
Canonical Event ──┬──► observer
                  ├──► test recorder
                  └──► service projection ──► bounded bridge ──► gRPC client
```

## 1. Event kinds

The runtime MUST expose semantic equivalents of:

```text
agent_start              the Run was accepted
turn_start               a Model Turn begins
message_start            a Message begins
message_update           streamed delta for the Message under construction
message_end              the Message is committed to the transcript
tool_execution_start     a resolved and validated Tool Use begins
tool_execution_update    bounded progress from a running Tool
tool_execution_end       a settled Tool outcome
toolset_activated        capability visibility changed
routine_started          a child Agent Run began
routine_completed        a child Agent Run settled successfully
routine_failed           a child Agent Run returned a terminal error
routine_cancelled        a child Agent Run was cancelled
turn_end                 the Turn and its Tool batch are finalized
agent_end                the Run is over
```

Go names and payload types MAY differ while preserving these lifecycle meanings.

## 2. Event classes

Every Event kind MUST belong to exactly one class.

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

A protected Event is a lifecycle transition or a settled outcome. Every consumer MUST receive each protected Event, in canonical order, or that consumer's stream MUST fail.

A coalescable Event is optional progress. A consumer under load MAY merge several into one, retain only the newest, or omit them.

A coalescable Event MUST NOT carry information absent from the protected Event that settles the same operation. This is what makes bounded delivery possible without losing history.

## 3. Ordering

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

Tool execution and Tool Result Events MUST occur after assistant `message_end` and before `turn_end`.

Parallel Tool batches complete out of order and their completion Events MUST reflect that. Transcript commitment MUST follow assistant source order:

```text
Completion Events   C → A → B     actual
Transcript          A → B → C     deterministic
```

Routine lifecycle Events MUST appear in the parent Run Event stream. Detailed child Events MUST preserve the child Run sequence.

## 4. Correlation

Every Event MUST carry sufficient correlation to place it:

```text
run_id
turn sequence
message or tool_call_id where applicable
routine_id · parent_run_id · child_run_id for child work
```

Consumers MAY add transport, request, or trace identifiers. Added identifiers MUST NOT replace runtime correlation.

## 5. Partial Messages

An assistant `message_update` MUST identify the Message under construction and carry a delta or partial value. The committed transcript changes at `message_end`, which MUST carry the authoritative Message.

A projection MAY omit cumulative snapshots from update Events so stream size stays linear in output length.

## 6. One terminal Event

A Run MUST emit exactly one `agent_end`. No loop Event MUST follow it for that Run.

Retry, compaction, and queued continuation MUST occur inside the Run, before the terminal Event. An implementation MUST NOT introduce a second completion signal that clients must wait for instead.

## 7. Observer contract

An observer runs inside the Run and is awaited before the loop proceeds.

```text
Canonical Event
      ↓
observer runs
      ↓
loop continues
```

Observers MUST run in registration order. Observer failure MUST follow one declared mode, blocking or advisory. Panics MUST be recovered at the observer boundary.

An observer MUST be in-process, fast, and Context-aware. An observer MUST NOT block on a network peer, a remote lock, or any wait without a bound of its own.

A remote consumer MUST NOT attach as an observer. Remote delivery MUST use the bounded bridge.

Observers MUST NOT alter Event identity, kind, order, or correlation.

## 8. Backpressure

Backpressure is what a system does when a producer outruns a consumer.

A Model can stream hundreds of deltas per second; a client on a poor connection may accept a handful. The gap admits only three answers:

```text
buffer without limit   memory grows until the process fails
discard silently       the consumer's history is wrong and cannot detect it
slow the producer      the producer pays for the consumer's speed
```

Every streaming path MUST choose explicitly. An unbounded queue MUST NOT be used as a default, because it selects the first answer without stating it.

## 9. Bounded bridge

Delivery across an asynchronous boundary MUST use a bounded bridge.

```text
Runtime goroutine                Sender goroutine
─────────────────                ────────────────
emit → observers → enqueue       dequeue → send
awaited · local · fast           slow · remote · bounded
```

A bridge MUST declare:

```text
capacity
which Event kinds are protected
how coalescing merges progress
behavior when the queue is full
how in-flight Events settle at shutdown
```

The enqueue side MUST participate in the producing Context, and the sender goroutine MUST belong to the consuming stream's Context. Neither side MUST be able to outlive the Context that authorized it.

Unbounded channels and detached sender goroutines MUST NOT be service defaults.

## 10. Queue-full policy

When capacity is reached, a bridge MUST apply one documented policy composed from:

```text
block        slow the producer within an explicit bound
coalesce     merge pending progress and retain the newest
terminate    fail the stream with a resource-exhausted error
```

Blocking MUST be bounded and Context-aware. An unbounded wait MUST NOT be used, because it lets one slow consumer hold a Run open indefinitely.

Coalescing MUST apply only to the coalescable class.

A bridge MUST NOT silently drop a protected Event. Terminating the stream is required instead: a consumer whose stream fails knows it lost history, while a consumer missing `tool_execution_end` does not.

## 11. Two settlements

```text
Execution settlement
  Model stream closed · Tool Uses finished
  Agent Routines settled · observers returned
  owned by the Runtime

Delivery settlement
  queue drained or abandoned · stream closed
  owned by the Service
```

They MUST be independent in both directions. A consumer that disconnects mid-Run ends delivery while execution proceeds to its own terminal Event. A Run that finishes quickly MAY still have Events in flight.

`WaitForIdle` MUST observe execution settlement. It MUST NOT wait on remote delivery.

## 12. Cancellation and disconnect

A disconnect ends delivery. It states nothing about intent.

```text
explicit Cancel   → cancel the Run Context
stream closed     → service policy decides
Run deadline      → cancel the Run Context
drain deadline    → cancel the Run Context
```

A service that treats a closed stream as cancellation MUST document that choice. Cancellation MUST then reach the Model, Tool Uses, observers, and every Agent Routine through the Run Context.

## 13. Drain

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

A bridge that cannot flush within its deadline MUST abandon delivery rather than delay shutdown. Execution history is already complete at that point; only its transmission is lost.

## 14. Service projection

A projection converts a canonical Event into a consumer-specific representation. It MAY filter, redact, re-encode, and enrich.

A projection MUST NOT delete or reorder canonical runtime history, and MUST preserve Event class and correlation.

Each consumer receives its own projection of the same immutable fact.

## 15. Acceptance

Tests MUST prove:

- canonical Event order matches state transitions;
- `agent_end` is the last Event of every Run;
- protected Events survive coalescing under load;
- a coalescable Event carries nothing its settling Event omits;
- an observer exceeding its bound is detected;
- a full queue applies its documented policy;
- a protected Event is never silently dropped;
- a disconnected consumer does not stall an unrelated Run;
- `WaitForIdle` returns without remote delivery;
- drain flushes or abandons within its deadline.
