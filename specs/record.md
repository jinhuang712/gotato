# The Record

A Session has to outlive its Instance, so Gotato keeps what happened in a Record, one Entry at a time, and never rewrites an Entry. The Session in memory is rebuilt from the Record, and nothing else holds the truth. This spec is the Record half of the `record` layer of the [map](ARCHITECTURE.md): how the log is laid out, what a rebuild reads, and how a Fork, a TaskSpec and a large Tool Result sit in it. Where the log is saved, and what keeps two writers apart, is in `store.md`. Compaction and the read-back tools, which are in the same package, are in `compaction.md`, and the Context the model sees is in `context.md`. The package `record` does not know where a Record is saved: `store` imports it, never the reverse.

## TL;DR
- A Record is an append-only log of Entries. Facts rebuild the Session, Log entries only say what happened, and every Entry is also an Event.
- A load starts at the last Checkpoint. A Checkpoint holds the Facts at one point and lists the Segments the Context uses, so a long Session opens as fast as a new one.
- A Session opened for a TaskSpec keeps that TaskSpec in its metadata, so every Instance rebuilds the Session the same.
- A Fork and a spilled result point into a log by position and epoch. They copy no body and never read another Record's future.

## The Record in memory

`record.Record` is the in-memory form of one Session's log, and the one a live Session commits to. It is a `gotato.Transcript` and a `gotato.Journal`: it holds the committed Messages, the Segments, the loaded Tools, the metadata and the Events, and every change queues an Entry. A Writer (in `store.md`) takes the queued Entries and writes them as one batch. `FromLoaded` rebuilds a Record from what a store returned, `View` from a whole log, and `Fork` from another Record. `Snapshot` gives a `Document`, a copy with no way to write, for export and tests. A Record also has an identity as a log, its Record epoch, chosen when it is created, so a number means the same Event only within one epoch.

## Facts and log entries

The Record is one line of Entries numbered from 1, and each Entry is a Fact or a Log entry.

```text
 Seq     1             2            3          4              5           6          7
         session_      session_     run_       message_       reply_      reply_     tool_
         created       claimed      started    committed      started     ended      started
 layer   FACT          FACT         log        FACT           log         FACT       FACT
         └ Checkpoint
```

Facts are what a rebuild reads, and one table in the code says which kinds they are, so a new kind that is forgotten fails its first write instead of quietly becoming a log entry a load leaves out:

| Facts | Log entries |
|---|---|
| `session_created`, `session_claimed` | `session_released`, `run_started`, `run_ended`, `turn_started`, `turn_ended` |
| `message_committed`, `notice_committed` | `message_queued`, `notice_queued`, `message_dropped`, `message_rejected`, `abort_requested` |
| `reply_ended`, `reply_cut_off` | `context_built`, `reply_started`, `reply_part_kept`, `model_retried` |
| `tool_started`, `tool_result_committed`, `tool_loaded`, `tool_unloaded` | `tool_ended`, `tool_result_spilled` |
| `metadata_changed`, `segment_closed`, `compaction_ended` (a Checkpoint) | `compaction_started`, `compaction_failed`, `extension_failed` |
 Log entries tell what happened, for people and telemetry: a Run's start and end, usage, model calls, the parts of a streaming reply. Splitting them keeps a load cheap, because its cost follows the state of the Session and not how chatty its Runs were. One exception: while a Run is in flight a load also returns the Log entries since the last Checkpoint, to rebuild the reply that was streaming as a Cut-off reply if the Run never ended. An Entry has a position, a kind (the Event's kind), a layer, a time, the Instance that wrote it and its data. A store reads nothing in it but the position, the kind and the layer.

## What a rebuild does

A Checkpoint stands in for everything before it, so a load starts at the last one.

```text
 Record   1 ... [seg 3][seg 4][CP 5: lists 3, 4 + tail] 6 ... 71 (head)
                                    └── Facts from here on ──┘
 rebuild  the Messages and metadata from the Checkpoint, then each later Fact
          the Segments the Checkpoint lists, by number, from their segment_closed Entries
          a Claim: the Instance and its Epoch; busy or released
          a Run that started and never ended: ended as failed, cause `interrupted`
          a Tool call that started and has no result: answered "outcome unknown";
          one that never started was not run
```

The Checkpoint names its Segments by number and does not copy them, which is why a load hands them back apart. How a compaction writes a Checkpoint is in `compaction.md`. A Claim is a Fact: the Instance that writes a Record names itself and its Epoch in an Entry at the position after the head it read, and the multi-instance spec covers how that makes one Writer.

## A TaskSpec in the Record

A Run of a TaskSpec (`RunRequest.Task` or `RunRequest.TaskSpec`), or a create of one (`CreateSessionRequest.Task` or `.TaskSpec`), opens a new Session, and the Record keeps the TaskSpec as it was.

```text
 session_created ─ metadata: gotato.agent     the TaskSpec's AgentProfile name
                            gotato.task_spec   the TaskSpec as a JSON document (profile.TaskSpecDoc)
```

The document is a metadata value like any other, written with the Session's first Entry and read with its facts, so it needs nothing from the Store. Any Instance that loads the Session decodes it and narrows the AgentProfile it looks up by name, whatever the registered spec says by then. A registered spec that changes reaches only new Sessions. Both keys are the Runner's (`ErrReservedKey` for a caller). How a TaskSpec is registered and narrowed is in `profile.md`.

## A Fork

A Fork is a new Record that starts from a point in another Record's log, and it writes the Store only.

```text
 parent "p"   1 ... 40 ... 58 (head when forked)
                           ▲
 fork "f"     1 session_created   forked_from {p, 58}, its own epoch
              2 segment_closed    inherited Segments, Source = p
              … session_claimed, session_released        ← one Create; the fork starts idle
```

The first Entry is a Checkpoint with the parent's tail, metadata and loaded tools, and the Entries after it carry the Segments the parent's Context used. A Segment keeps its number and names the parent as its Source, with the parent's epoch, so the original Messages are read from the parent's log. Nothing of the parent written after position 58 reaches the fork. No Instance activates anything: whoever receives the fork's first request loads it. A Tool call of the parent's Run that had no result yet is answered in the fork as not finished at the time of the fork, since the parent's writer may be alive and about to answer it.

## A spilled result

A Tool Result over one page is stored whole as an Entry of its own, and the Message keeps a preview and a pointer.

```text
 Record   ... 11 tool_started │ 12 tool_result_spilled        │ ... 15 tool_result_committed
                              │    call_id, body (412 KiB)    │       preview + ref result:<call_id>
                              ▲                               │       Spilled{seq: 12, bytes}
                              └───────────────────────────────┘ the Message points back by position
 read_result(ref, page) ─▶ Records.Log(after=11, limit=1) ─▶ the body, one page at a time
```

The body is written before the Message, so the Message can name its position. A crash between the two leaves a Log entry nobody points to, and nothing else. It is a Log entry, so a load does not read it, except while a Run is in flight, when the load also returns the Log entries since the last Checkpoint. A Session with many large results therefore loads as fast as one with none. The Message is written once and stays small, so the bytes already sent do not change. A fork keeps the parent's Session and epoch in the pointer, so a parent that was deleted and created again reads as pruned and not as another body. The body is left out of the Events a subscriber gets and is read only by `read_result`.

## What this buys you
- Any Instance rebuilds the same Session from the Store, at the cost of the part since the last Checkpoint.
- Large results and forks cost the Store one Entry each, not a copy.
- A Session knows what a crash left: a Run that never ended, and a Tool call whose outcome is unknown.

## See also
[ARCHITECTURE.md](ARCHITECTURE.md), the principles [the store is the only truth](../DESIGNS.md#1-the-store-is-the-only-truth) and [no instance is special](../DESIGNS.md#8-no-instance-is-special), [FEATURES.md](../FEATURES.md) (F8: Store, F3.3.4: A task Session keeps its spec, F6.7: Large results), decisions [002](../plan/decisions/002-the-store-is-the-truth-and-memory-is-a-cache.md), [017](../plan/decisions/017-the-record-is-an-append-only-log.md), [028](../plan/decisions/028-every-step-is-written-as-facts-and-a-log.md), [030](../plan/decisions/030-every-entry-is-an-event.md), [040](../plan/decisions/040-a-large-tool-result-is-spilled-and-read-in-pages.md), [044](../plan/decisions/044-an-agent-profile-and-a-task-spec-are-registered-inline-or-from-a-store.md) and [050](../plan/decisions/050-each-package-is-one-layer-named-for-its-job.md).
