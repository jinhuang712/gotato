# The Record

A Session has to outlive its Instance: the process can stop between two Steps, and any other Instance must be able to pick the Session up. So Gotato keeps what happened in a Record in the Store, one Entry at a time, and never rewrites an Entry. The Session in memory is rebuilt from the Record, and nothing else holds the truth. This spec shows how the Record is laid out, what the Store promises, and what keeps two writers, or two lives of one ID, apart.

## TL;DR
- A Record is an append-only log of Entries. Facts rebuild the Session, Log entries only say what happened, and every Entry is also an Event.
- The Store makes one promise: a batch lands whole, at the position after the head the Writer read, or it is refused. The Record epoch refuses a Writer of a Record that was deleted.
- A Fork and a spilled result point into a log by position and epoch. They copy no body and never read another Record's future.

## Facts and log entries

The Record is one line of Entries numbered from 1, and each Entry is a Fact or a Log entry.

```text
 Seq     1             2            3          4              5           6          7
         session_      session_     run_       message_       reply_      reply_     tool_
         created       claimed      started    committed      started     ended      started
 layer   FACT          FACT         log        FACT           log         FACT       FACT
         └ Checkpoint
```

Facts are what a rebuild reads: Messages, Tool calls that started, metadata changes, loaded tools, Segments, Claims and Checkpoints. Log entries tell what happened, for people and telemetry: a Run's start and end, usage, model calls, the parts of a streaming reply. Splitting them keeps a load cheap, because its cost follows the state of the Session and not how chatty its Runs were. One exception: when the last Run never ended, a load also reads the Log entries since the last Checkpoint, to rebuild the reply that was streaming as a Cut-off reply.

## What a load reads

A load starts at the last Checkpoint and skips every Entry before it, except the Segments that Checkpoint lists.

```text
 Record   1 ... [seg 3][seg 4][CP 5: lists 3, 4 + tail] 6 ... 71 (head)
                   │      │            └── Facts from here on ──┘
                   └──────┴── segment_closed Entries before the Checkpoint

 Store.Load  ─▶  Facts     the Facts from the last Checkpoint on (Log entries left out)
                 Segments  the segment_closed Entries before it
                 Head      the position of the last Entry
                 Info      the RecordInfo: counts, usage, the Run in flight, metadata
```

A Checkpoint holds the state of the Facts at one point, so it stands in for everything before it. It names its Segments by number and does not copy them, which is why `Load` hands them back apart. The RecordInfo travels with every write, so `List` never has to read a log. How a compaction writes a Checkpoint is in [compaction.md](compaction.md).

## One write

Every write is one conditional write, so a plain SQL or document database can keep the contract.

```text
 Writer A (holds the Claim)               Store               Writer B (stale)
   │ Append(after=41, Entries 42-44) ───▶  │
   │                                       │ last position is 41: write all three
   │ ◀─────────────────── ok ───────────── │
   │                                       │ ◀── Append(after=41, Entry 42) ──────│
   │                                       │ last position is 44, not 41
   │                                       │ ──────── ErrConflict ───────────────▶│
```

An Append writes its batch all or nothing, at `after+1` onward, so an adapter maps it to an insert on a unique (session, seq) key. A Step is one batch, so a crash loses at most the Step in flight. Create is the same write for a new Record: its first Entry is a Checkpoint at Seq 1, and a taken ID is `ErrExists`. A Writer stops at its first failed write. A conflict means another writer took the Record; any other failure leaves the outcome unknown, so the Record is opened again from the Store to see whether the write landed.

## Two fences on every write

An Append is checked in this order, and the epoch comes before the position.

```text
 Append(id, after, Entries, RecordInfo)
   0  RecordInfo names a Record epoch?     no ─▶ invalid_argument
   1  the Record exists?                   no ─▶ ErrNotFound
   2  the Record epoch is the stored one?  no ─▶ ErrNotFound  (deleted and created again)
   3  `after` is the last position?        no ─▶ ErrConflict  (another writer wrote first)
   4  write the batch, replace the RecordInfo
```

Fence 3 is the position. A Claim is an Entry written at the position after the head its Instance read, so a Writer that lost its Claim always writes to a taken position. Fence 2 is the Record epoch, an identity chosen when the Record is created. Without it, a writer of a deleted Record could land at a position that is free again in the new one. The Epoch inside a Claim is another number, and the multi-instance spec covers it.

## Delete and create again

A number means the same Event only within one Record epoch, so an ID that is reused starts a new epoch.

```text
 Record "s1"   epoch E1: 1 2 3 ... 40 41 │ deleted │ epoch E2: 1 2 3 ...
 Writer W1 (E1)                    Append(E1) ────────────▶ ErrNotFound
 Client C (saw E1, up to 40)                resume(E1, 40) ─▶ cannot_resume, with E2 and the head
```

The old Writer is refused even when the new log is as long as its own, because the epoch is compared first. The file store goes further: the epoch is in the path, so the old Writer finds no directory to write into. A client that sends the epoch it saw is refused instead of being handed another conversation's 41st Event. A delete claims the Session first, so it never removes a Record from under a Run on another Instance.

## A Fork

A Fork is a new Record that starts from a point in another Record's log, and it writes the Store only.

```text
 parent "p"   1 ... 40 ... 58 (head when forked)
                           ▲
 fork "f"     1 session_created   forked_from {p, 58}, its own epoch
              2 segment_closed    inherited Segments, Source = p
              … session_claimed, session_released        ← one Create; the fork starts idle
```

The first Entry is a Checkpoint with the parent's tail, metadata and loaded tools, and the Entries after it carry the Segments the parent's Context used. A Segment keeps its number and names the parent as its Source, with the parent's epoch, so the original Messages are read from the parent's log. Nothing of the parent written after position 58 reaches the fork. No Instance activates anything: whoever receives the fork's first request loads it.

## A spilled result

A Tool Result over one page is stored whole as an Entry of its own, and the Message keeps a preview and a pointer.

```text
 Record   ... 11 tool_started │ 12 tool_result_spilled        │ ... 15 tool_result_committed
                              │    call_id, body (412 KiB)    │       preview + ref result:<call_id>
                              ▲                               │       Spilled{seq: 12, bytes}
                              └───────────────────────────────┘ the Message points back by position
 read_result(ref, page) ─▶ Store.Log(after=11, limit=1) ─▶ the body, one page at a time
```

The body is written before the Message, so the Message can name its position. A crash between the two leaves a Log entry nobody points to, and nothing else. It is a Log entry, so a load never reads it, and a Session with many large results loads as fast as one with none. The Message is written once and stays small, so the bytes already sent do not change. A fork keeps the parent's Session and epoch in the pointer, so a parent that was deleted and created again reads as pruned and not as another body.

## What this buys you
- Any Instance rebuilds the same Session from the Store, at the cost of the part since the last Checkpoint.
- A stale Writer, or the Writer of a deleted Record, cannot corrupt a Record. It is refused before it writes.
- Large results and forks cost the Store one Entry each, not a copy.

## See also
[FEATURES.md → G-F03](../FEATURES.md#g-f03--stores-) and [G-F35](../FEATURES.md#g-f35--large-tool-results-), decisions [002](../plan/decisions/002-the-store-is-the-truth-and-memory-is-a-cache.md), [006](../plan/decisions/006-a-conformance-suite-ships-with-the-store-contract.md), [010](../plan/decisions/010-the-record-is-an-append-only-log.md), [011](../plan/decisions/011-one-writer-per-session-claimed-in-the-store.md), [014](../plan/decisions/014-every-step-is-written-as-facts-and-a-log.md), [015](../plan/decisions/015-every-entry-is-an-event.md) and [025](../plan/decisions/025-a-large-tool-result-is-spilled-and-read-in-pages.md), [GLOSSARY.md](../GLOSSARY.md). Specs to come: events, multi-instance.
