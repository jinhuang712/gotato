# Compaction

## TL;DR
- A compaction closes the Messages since the last Checkpoint, less a kept tail, into a Segment; one batch writes the Segment and a Checkpoint.
- Code builds the Segment; the model writes only the Recap, which may be absent.
- Past their budget, the oldest Segments merge. A failed compaction never half-writes.

## The Record over time

The Record only grows; a load starts from the last Checkpoint and reads nothing before it.

```text
Record  Messages ... │CP 1│ Seg 1 │ Messages ... │CP 2│ Seg 2 │ tail │ new Messages ─▶
                     ▲                           ▲
                     └ not read                  └ last Checkpoint: a load starts here
```

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

## A merge

The oldest Segments become one at the front, with one Recap and a Checkpoint of their own; `read_segment` still reads 1 to 3.

```text
before   [Seg 1][Seg 2][Seg 3][Seg 4][Seg 5] tail
after    [Seg 1-3, one Recap][Seg 4][Seg 5] tail
          └─ new bytes ─────┘ └─ unchanged ─┘
```

## Size against the ceiling

Four triggers compact a Session; each is recorded as the compaction's `why`.

```text
tokens ▲  (a compaction drops the size to Segments + tail)
       │                                   ✕ overflow: the provider refused the request
ceiling┼ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─
       │                         ● past_ceiling: checked before each model call
       │            ● idle: a warm Session is about to lose its cache (before_unload)
       │   ● on_demand: the service asks, at any size
```

## When it fails

A crash leaves no half-compaction: the Context is what the last Checkpoint lists.

```text
 step         1 cut   2 started   3 Recap   4 build   5 write    6 unload   7 merge
 a crash      nothing  an Event    nothing   nothing   all or     tools stay Segment
 here leaves           only                            none       loaded     unmerged
┌─────────────────┬──────────────────────────────────────────────────────────┐
│ Recap fails     │ the Segment is kept without a Recap (recap_error)        │
│ write fails     │ the compaction is dropped; the Record is as it was       │
│ overflow        │ compact and send again, once per Turn; refused twice:    │
│ (twice)         │ context_overflow; still past the ceiling: limit_exceeded │
└─────────────────┴──────────────────────────────────────────────────────────┘
```

## See also
[FEATURES.md → G-F06](../FEATURES.md#g-f06--compaction-), decisions [018](../plan/decisions/018-compaction-closes-a-segment-and-never-rewrites-one.md) and [019](../plan/decisions/019-a-segment-is-written-once.md), [GLOSSARY.md](../GLOSSARY.md).
