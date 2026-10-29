# Compaction

A Session grows with every Turn, and the model can read only so much. Compaction keeps the Context inside that limit without losing the history. It closes the old Messages into a Segment, a compact copy that stays on the Record. Then the Context holds the Segments and the newest Messages, not everything.

## TL;DR
- A compaction closes the Messages since the last Checkpoint, less a kept tail, into a Segment; one batch writes the Segment and a Checkpoint.
- Code builds the Segment; the model writes only the Recap, which may be absent.
- Past their budget, the oldest Segments merge. A failed compaction never half-writes.

## The Record over time

The Record only grows; a load starts from the last Checkpoint and reads nothing before it.

```text
Record  Messages ... │Seg 1│CP 1│ Messages ... │Seg 2│CP 2│ tail │ new Messages ─▶
                                                       ▲
        read by nobody: before the last Checkpoint     └ last Checkpoint: a load starts here
```

This is the whole history, left to right. Nothing is ever rewritten, so a compaction is an append, and the old Messages stay for anyone who wants to read them. A Segment is written before the Checkpoint that lists it, so a Checkpoint never points at a Segment that is not there. Watch for the word "load": a new Instance reads from the last Checkpoint only, which is why a long Session still opens fast.

## What the model is sent

The Context is a stack; a compaction changes the bottom and leaves the top as it was.

```text
   before                      after one compaction
┌──────────────────┐        ┌──────────────────┐
│ system + tools   │        │ system + tools   │ ┐ same bytes:
│ Segment 1        │        │ Segment 1        │ │ the provider's
│ Segment 2        │        │ Segment 2        │ ┘ cache prefix
├──────────────────┤        ├──────────────────┤
│ Messages since   │ ─────▶ │ Segment 3 (new)  │ written once
│ the Checkpoint   │        │ kept tail        │
└──────────────────┘        └──────────────────┘
```

On the left is what the model sees now; on the right, what it sees next. The top block is byte for byte the same, because the provider's prompt cache matches on a prefix, and one changed byte near the top would make the next request pay full price. That is why a Segment is written once and never edited. The kept tail is the newest Messages, left whole so the model keeps the thread of the last exchange. In the middle of a Run it always holds the Run's last reply and everything committed after it (tool results, a notice, a steer), since the next model call continues from there; on the Run's first Turn it holds the prompt whole.

## One compaction

Steps run top to bottom; only step 5 writes, so only it makes the compaction real.

```text
 Loop / hook                  Recap model              Store
     │ 1 cut: pick the kept tail   │                      │
     │ 2 compaction_started        │                      │
     │ 3 ──── Recap request ─────▶ │                      │
     │   ◀── Recap, or an error ── │                      │
     │ 4 build the Segment in code │                      │
     │ 5 segment_closed + Checkpoint ── one batch ──────▶ │  ◀ the only write
     │ 6 tool_unloaded, one per tool not kept             │
     │ 7 merge, if the Segments pass their budget         │
```

Code does almost everything here: it cuts, copies the Messages into the Segment and decides which tools stay loaded. The model is asked for one thing, the Recap, a short note of what the closed Messages said. Keeping the model's part that small means a bad answer cannot damage the Segment; at worst the Recap is missing. The Segment and the Checkpoint go in one batch, so either both exist or neither does. Steps 6 and 7 come after the write and only report or tidy up.

## A merge

The oldest Segments become one at the front, with one Recap and a Checkpoint of their own; `read_segment` still reads 1 to 3.

```text
before   [Seg 1][Seg 2][Seg 3][Seg 4][Seg 5] tail
after    [Seg 1-3, one Recap][Seg 4][Seg 5] tail
          └─ new bytes ─────┘ └─ unchanged ─┘
```

Segments pile up, and each costs tokens every Turn, so past their budget the oldest are folded into one. The merge makes a new Segment and a new Checkpoint; it does not edit anything. Only the front of the Context changes, and the Segments after it keep their bytes, so the cache loses as little as it can. The merged Segment keeps the numbers it replaced, so the model can still open Segment 2 and read the original.

## Size against the ceiling

These triggers compact a Session; each is recorded as the compaction's `why`.

```text
tokens ▲  (a compaction drops the size to Segments + tail)
       │
ceiling┼ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─
       │                         ● past_ceiling: checked before each model call
       │            ● before_unload / before_cache_expires: a warm Session goes idle
       │   ● on_demand: the service asks, at any size
       │
       │   ✕ overflow: the provider refused the request as too long, at any size
```

The first three compact on purpose: when the size crosses the ceiling, when an idle Session is about to lose its cache, and when the service asks. Compacting before the cache expires makes the first request after a pause small and cheap. An overflow is different: it is the provider saying no, so it can happen below the ceiling when our size estimate was low. A Turn compacts at most once, so if the retry is refused again the Run ends instead of looping.

## When it fails

A crash leaves no half-compaction: the Context is what the last Checkpoint lists.

```text
 step         1 cut   2 started   3 Recap   4 build   5 write    6 unload   7 merge
 a crash      nothing  an Event    nothing   nothing   all or     the Event  Segments
 here leaves           only                            none       is lost    unmerged
┌─────────────────┬──────────────────────────────────────────────────────────┐
│ Recap fails     │ the Segment is kept without a Recap (recap_error)        │
│ write fails     │ the compaction is dropped; the Record is as it was       │
│ overflow        │ compact and send again, once per Turn; refused twice:    │
│ (twice)         │ context_overflow; still past the ceiling: limit_exceeded │
└─────────────────┴──────────────────────────────────────────────────────────┘
```

The top row is a crash at each step. Only step 5 changes the Record, and it is one batch, so a crash before it changes nothing and a crash after it leaves a finished compaction. The kept tools are part of the Checkpoint, so a crash at step 6 loses only the `tool_unloaded` Events, not the choice. The table is a failure with no crash. A compaction helps a Run but is not a step the Run needs, so a failure is dropped and the Run goes on, unless the Context is still too big to send.

## What this buys you
- The first request after a pause is cheap: the Session compacted before its cache expired, and the top of the Context did not change.
- A Run never depends on a compaction: a failed one leaves the Record as it was and the Run carries on.
- Any Instance rebuilds the same request: the Context comes from the Record and its last Checkpoint, so whoever loads it builds the same bytes.

## See also
[FEATURES.md → G-F06](../FEATURES.md#g-f06--compaction-), decisions [032](../plan/decisions/032-compaction-closes-a-segment-and-never-rewrites-one.md) [034](../plan/decisions/034-a-segment-is-written-once.md) and [028](../plan/decisions/028-every-step-is-written-as-facts-and-a-log.md), [GLOSSARY.md](../GLOSSARY.md). Related specs: [context.md](context.md), [record.md](record.md) and [session.md](session.md).
