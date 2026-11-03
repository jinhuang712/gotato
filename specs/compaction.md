# Compaction

A Session grows with every Turn, and the model can read only so much. Compaction keeps the Context inside that limit without losing the history. It closes the old Messages into a Segment, a compact copy that stays on the Record. Then the Context holds the Segments and the newest Messages, not everything. This spec is the compaction half of the `record` layer of the [map](ARCHITECTURE.md), with the form of a reference in the `ref` layer. It is run by a Turn preparer the host installs, and the Context it changes is described in `context.md`.

## TL;DR
- A compaction closes the Messages since the last Checkpoint, less a kept tail, into a Segment; one batch writes the Segment and a Checkpoint.
- Code builds the Segment; the model writes only the Recap, which may be absent.
- Past their budget, the oldest Segments merge. A failed compaction never half-writes.
- What a Segment leaves out stays readable: each tool call line and each kept message carries a ref, and `read_result` returns the original from the Record.
- A fixed frame, `record.Compactor`, runs the steps and alone builds what is written. A user replaces a step (when, where to cut, how a Segment is built, the Recap, how many merge) without copying the rest, and the frame keeps every guarantee whatever the step returns.

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

Steps run top to bottom; only step 5 puts anything in the log, so only it makes the compaction real. The frame runs every step; the Cutter (1), the Recap writer (3) and the Builder (4) are parts a user can replace.

```text
 Loop / hook                  Recap model              Store
     │ 1 cut: the Cutter picks the kept tail              │
     │ 2 compaction_started        │                      │
     │ 3 ──── Recap request ─────▶ │                      │
     │   ◀── Recap, or an error ── │                      │
     │ 4 the Builder builds the Segment                   │
     │ 5 segment_closed + Checkpoint ── one batch ──────▶ │  ◀ the only write
     │ 6 tool_unloaded, one per tool not kept             │
     │ 7 merge, if the Segments pass their budget         │
```

Code does almost everything here: it cuts, copies the Messages into the Segment and decides which tools stay loaded. The model is asked for one thing, the Recap, a short note of what the closed Messages said. Keeping the model's part that small means a bad answer cannot damage the Segment; at worst the Recap is missing. The Segment and the Checkpoint go in one batch, so either both exist or neither does. With the default `SegmentStore`, the Record, the two Entries are queued under one lock and the Writer writes them in one append at its next write, which is the next Step of the Turn. Steps 6 and 7 come after the write and only report or tidy up. Before step 1 the Trigger said a compaction is due. Between the steps the frame checks what each part returned, and stamps the Segment; the next section says how.

## The frame and its parts

A compaction is a frame around parts. The frame runs the parts in a fixed order and is the only thing that writes; a part returns a value.

```text
              ┌──────────────── Compactor (the frame) ─────────────────┐
 TurnState ─▶ │ Trigger ─▶ Cutter ─▶ Recap ─▶ Builder ─▶ check, stamp  │
              │ (due?)    (cut at)  (text)   (Segment)       │         │
              │ Picker ─▶ Recap or digest ─▶ check, stamp    │ (merge) │
              └──────────────────────────────────────────────┼─────────┘
                                                             ▼
                                   SegmentStore: one batch per call (the only write)
```

Read it left to right. The Trigger answers "is a compaction due, and why". The Cutter answers "where does the tail start". The Recap writer, if there is one, writes the note. The Builder renders the closed Messages as a Segment. For a merge, the Picker answers "how many of the oldest", and the Recap or a digest ends the merged Segment. Each part hands a value back to the frame; none of them touches the store. The frame checks every value, stamps what only it may stamp, and writes through the `SegmentStore`.

| Part | Does | Default |
|---|---|---|
| `SegmentStore` | the Context's Segments, tail and loaded Tools, and the two batches `CloseSegment` and `MergeSegments` | `*record.Record`; any Transcript can implement it |
| `Trigger` | `Due(ctx, TurnState) (why, bool)`: when a compaction is due | `CeilingTrigger`: overflow, past the ceiling, or Segments past budget on a Run's first Turn (`past_budget`, which merges only) |
| `Cutter` | `Cut(stretch, CutInfo) int`: where the tail starts | `TailBudget{KeepMessages, TailTokens}` |
| `Builder` | `Build(closed, SegmentInfo) (Segment, error)`: the words of the Segment | `CodeSegments{SegmentSettings}` |
| `RecapWriter`, `MergeRecapWriter` | the Recap of a Segment, and of a merge | none: no Recap, and a merge gets the digest |
| `Picker` | `Pick(segments, budgetTokens) int`: how many oldest Segments merge | `OldestToHalf` |

`record.Compactor{CompactPolicy, Trigger, Cut, Builder, Recap, Merge}` holds them. A nil part is its default, so the zero `Compactor` behaves as compaction did before the parts were named. `CompactPolicy` keeps only the numbers: `CeilingTokens`, `WindowTokens`, `DropReasoning`, `SegmentBudgetTokens` and `Location` (the time zone of Segment times, digests, Recap asks and `read_segment`).

What the frame keeps, whatever a part returns:

| Rule | What the frame does |
|---|---|
| Writes | One batch per `SegmentStore` call. A part that fails, panics or breaks a rule drops the compaction (`compaction_started`, then `compaction_failed` with the cause `extension_failure`, nothing written) and never the Run. A part gets copies of its arguments, so it cannot change what the frame holds. |
| Principle 2 | The store lists every Segment of the Context, so none is dropped or moved. The tail is `stretch[cut:]` as committed, less reasoning (`DropReasoning`) and usage. The cut must be inside the stretch, else the compaction fails; the frame moves it forward to a Turn boundary, so a tool call keeps its result, and, mid-Run, back to keep the Run's last Turn. A merge replaces two or more of the first Segments. |
| Decisions 034 and 043 | The frame stamps `Number`, `From`, `To`, `Source`, `Merged` and the Message IDs `segment-<n>-<i>`, and appends the Recap escaped. It refuses a Segment with a tagged Part (one of the four tags) whose text holds a tag or is not of the form `<tag ...>...</tag>`, a Message that is not a plain user or assistant Message (no tool calls, results or notices, and at least one Message), and a ref that names nothing the Segment closed. So every item stays readable by `read_segment(n)`. |
| Per Turn | A Turn compacts at most once, whatever the Trigger says. What the Run staged is flushed before the write. A Run still past the ceiling after its compaction ends `limit_exceeded` ([038](../plan/decisions/038-a-limit-guards-a-real-failure.md)). |

A ref is checked in the two shapes the default Builder writes: `[result:<call_id> ...]` ending a tool call line inside `<tool_calls>`, and a line `ref msg:<message_id>` inside `<segment>`. A Trigger that panics, or says "due" with no reason, drops that Turn's compaction and publishes an `extension_failed` Event (stage `compaction_trigger`).

The line between the two is who can be trusted with what. The frame does what a mistake would turn into lost history or a cold cache: the write, the numbers, the escaping, the cut at a Turn boundary. A part does what a team has a view on: when, where, what to say. To run one, `record.Compact(ctx, st, compactor, cfg)` compacts once and `record.AutoCompact(st, compactor)` is the `TurnPreparer`; in a service, `profile.AgentProfile.Compact` is the `Compactor`. A `CompactConfig` changes the numbers, the kept tail (`KeepMessages`, as `TailBudget`) and the Recap writer for one call.  The guide on extension points has an example of each part.

## A merge

The oldest Segments become one at the front, with one Recap and a Checkpoint of their own; `read_segment` still reads 1 to 3. The Picker says how many of the oldest merge: two or more, at most all of them.

```text
before   [Seg 1][Seg 2][Seg 3][Seg 4][Seg 5] tail
after    [Seg 1-3, one Recap][Seg 4][Seg 5] tail
          └─ new bytes ─────┘ └─ unchanged ─┘
```

Segments pile up, and each costs tokens every Turn, so past their budget the oldest are folded into one. The merge makes a new Segment and a new Checkpoint; it does not edit anything. Only the front of the Context changes, and the Segments after it keep their bytes, so the cache loses as little as it can. The merged Segment keeps the numbers it replaced, so the model can still open Segment 2 and read the original. The refs go through the merge: the digest built by code keeps the refs of what each line stands for, and the Recap the model writes is asked from the earlier Recaps with their refs and told to keep each next to what it states, so a line of the front Segment still has a way back to its original after many merges. A ref a Recap lacks is not an error. The default Picker, `OldestToHalf`, folds the oldest until the rest fit half the budget, and at least two. A Picker that answers 0 merges nothing; one that answers 1, or more than there are, fails the merge with `pick_failed` and nothing is merged. The digest ends the merged Segment only when no merge Recap writer is set: a merge whose Recap fails merges nothing (`recap_failed`), as does one whose write fails (`write_failed`), and the Segments stay as they were.

## What a Segment keeps, and the way back

A Segment shows the user's messages, each Run's final reply and one line per tool call; it leaves out tool results, a Run's other text and reasoning. Each line names what it stands for, so the model can ask for one item.

```text
10:02 payments.get_batch {"batch_id":"B-1"} → ok [result:c1]     result:<call_id>
Some charges in last night's batch failed. Why?
<segment n="1">  ref msg:m01  </segment>                          msg:<message_id>
```

`read_result(ref)` returns the original of either, page by page, from the Record: a result that was never spilled is read the same way as one that was. Reasoning gets no ref and is not read back. A ref points at the Record, so every instance resolves it alike, and into Entries a team pruned it reads as removed. A Segment written by an older version has no refs and renders as it did; a Segment is still written once.

The ref form is a contract, since a Segment carries it forever, so it lives in the small public package `ref`: `ref.Result(call)` is `result:<call_id>`, `ref.Message(id)` is `msg:<message_id>`, with `ref.Parse` and `ref.Safe`, and the two tools are `ref.ReadResult` and `ref.ReadSegment`. A Builder of a team's own may write refs, and the frame refuses one that names nothing the Segment closed, so no Segment points at a thing the model cannot read. Where the originals are read from is the `ReadBack`'s `Originals`, described with the tools in `tools.md`.

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

The first three compact on purpose: when the size crosses the ceiling, when an idle Session is about to lose its cache, and when the service asks. Compacting before the cache expires makes the first request after a pause small and cheap. An overflow is different: it is the provider saying no, so it can happen below the ceiling when our size estimate was low. A Turn compacts at most once, so if the retry is refused again the Run ends instead of looping. What counts is a compaction or a merge that started (it emitted `compaction_started`), even one that was dropped; a check that found nothing to close or merge does not count.

## When it fails

A crash leaves no half-compaction: the Context is what the last Checkpoint lists.

```text
 step         1 cut   2 started   3 Recap   4 build   5 write    6 unload   7 merge
 a crash      nothing  an Event    nothing   nothing   all or     the Event  Segments
 here leaves           only                            none       is lost    unmerged
┌─────────────────┬──────────────────────────────────────────────────────────┐
│ Recap fails     │ the Segment is kept without a Recap (recap_error)        │
│ write refused   │ the Store keeps nothing of the batch; the Writer stops   │
│                 │ and the Run ends store_failure (see below)               │
│ a part fails,   │ the compaction is dropped (extension_failure); nothing   │
│ panics or breaks│ is written; the Run goes on                              │
│ a rule          │                                                          │
│ overflow        │ compact and send again, once per Turn; refused twice:    │
│ (twice)         │ context_overflow; still past the ceiling: limit_exceeded │
└─────────────────┴──────────────────────────────────────────────────────────┘
```

The top row is a crash at each step. Only step 5 changes the Record, and it is one batch, so a crash before it changes nothing and a crash after it leaves a finished compaction. The kept tools are part of the Checkpoint, so a crash at step 6 loses only the `tool_unloaded` Events, not the choice. The table is a failure with no crash. A compaction helps a Run but is not a step the Run needs, so a failure of a part, of the Recap or of the build is dropped and the Run goes on, unless the Context is still too big to send. A refused write is different, because the default `SegmentStore` only queues the batch: the Store still holds nothing of it, so the Record is as it was, but the Writer fails on its first failed write like any other, and the Run ends `store_failure`. Only a `SegmentStore` that writes the batch itself can drop the compaction and let the Run go on. A part is code of the team's, so it is held to the same rule: a Cutter whose index is outside the stretch, a Builder whose Segment has a tag in its text or a ref to nothing, a Picker that answers 1, or any part that returns an error or panics, drops that compaction (`ErrBadPart`, code `extension_failure`) and the Record is as it was. The Recap writer keeps its own rule: it fails alone, and the Segment is kept without a Recap.

The ceiling is a hard line. After a Turn's compaction for the ceiling the size is estimated afresh (the Messages plus the instruction and tool definitions) and compared with the ceiling; a Run still past it ends `limit_exceeded`, whoever set the ceiling. The check does not run for an overflow Turn, which ends `context_overflow` when the provider refuses it again, or for a merge alone, or with no ceiling in force. It is not a cost knob: a team that wants a cost cap writes its own `TurnStopper` (decision [038](../plan/decisions/038-a-limit-guards-a-real-failure.md)). A ceiling below the window must stay above what a compaction cannot remove: the instruction and tool definitions, the kept tail (20,000 tokens by default, capped at a quarter of the window, or of the ceiling when the window is unknown; `KeepMessages` has no cap), a Recap if any, and the largest Tool Result one Turn can hold (under the 32 KiB page, about 8K tokens). This is a rule of thumb and is not measured.

## What this buys you
- The first request after a pause is cheap: the Session compacted before its cache expired, and the top of the Context did not change.
- A Run never depends on a compaction: one that fails in a part, the Recap or the build leaves the Record as it was and the Run carries on, and a refused write never leaves half of one.
- Any Instance rebuilds the same request: the Context comes from the Record and its last Checkpoint, so whoever loads it builds the same bytes.

## See also
[ARCHITECTURE.md](ARCHITECTURE.md), the principles [a request only grows at its end](../DESIGNS.md#2-a-request-only-grows-at-its-end) and [the Context is a bounded view](../DESIGNS.md#3-the-context-is-a-bounded-view), [FEATURES.md](../FEATURES.md) (F5: Compaction), decisions [032](../plan/decisions/032-compaction-closes-a-segment-and-never-rewrites-one.md), [033](../plan/decisions/033-code-builds-a-segment-and-the-model-writes-only-the-recap.md), [034](../plan/decisions/034-a-segment-is-written-once.md), [038](../plan/decisions/038-a-limit-guards-a-real-failure.md), [043](../plan/decisions/043-compaction-moves-content-out-and-never-makes-it-unreadable.md) and [046](../plan/decisions/046-compaction-is-a-fixed-frame-around-parts-a-user-can-replace.md).
