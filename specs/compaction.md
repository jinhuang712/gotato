# Compaction

How a Session keeps what the model is sent within its window, and keeps the first request after a long pause cheap. This document describes the mechanism: what always holds, the steps in order, and what happens when a step fails. What each setting defaults to is in [FEATURES.md → G-F06](../FEATURES.md#g-f06--compaction-); why it was chosen is in the decisions linked at the end.

## TL;DR

- A compaction **closes** what came since the last Checkpoint, less a kept tail, into a **Segment**, and appends the Segment and a **Checkpoint** that lists it in one step. The Record only grows; nothing is rewritten.
- **Code builds the Segment** (the user's words, each Run's final reply, one line per tool call). The Agent's model writes only the **Recap** at its end, and a Segment without a Recap is still valid.
- The Segments before keep their bytes, so a request only grows at its end. When the Segments pass their budget, the oldest **merge** into one merged Segment at the front.
- Four triggers: **idle** (before the cache goes cold), **past the ceiling**, **overflow** (the provider refused the request as too long) and **on demand**. A Turn compacts at most once.
- A compaction helps a Run but is not a step it needs: its failure stays local, and the Run goes on or ends with a code that says why.

## Terms

| Term | Meaning here |
|---|---|
| Segment | one stretch of history a compaction closed, stored once as a `segment_closed` Entry and never rewritten |
| Checkpoint | the `compaction_ended` Event: it lists the Segments the Context uses, holds the kept tail and the loaded tools, and a load starts from it |
| Kept tail | the Messages after the cut, which the Context carries as they are |
| Recap | the last part of a Segment, written by the Agent's model: five fixed `## ` headings |
| Ceiling | the estimated size past which a Session compacts; the window less a reserve unless set |
| Budget | the most the Segments together may hold before the oldest merge |

## The shape of the Context

Each compaction appends one Segment after the Segments before it. This shows the Context before and after the third compaction:

```text
before   [Segment 1][Segment 2] the Messages since the last Checkpoint …
after    [Segment 1][Segment 2][Segment 3] the kept tail
```

The model is sent this, from the top. Everything above the tail is the same bytes as before the compaction, except the new Segment:

```text
system     the Agent's instruction as it is now, and the fixed sentence about the tags
tools      the base tools, the loaded tools, and read_segment
Segment 1  <segment> · user · <tool_calls> · assistant · … · <recap>    never rewritten
Segment 2  <segment> · user · <tool_calls> · assistant · … · <recap>    never rewritten
Segment 3  <segment> · user · <tool_calls> · assistant · … · <recap>    never rewritten
tail       the Messages since the cut, as they are
```

## Invariants

### 1. A Segment is written once

A Segment is appended as its own `segment_closed` Entry, holding its data (number, first and last time, the range of Entries it covers, its tool call lines, its Recap, the tools it used) and its rendered Messages. Those bytes never change, because a provider's prompt cache reads them. The rules that turn the data into a request are fixed, so every Instance sends the same bytes. Rendering at request time, or copying the Messages into each Checkpoint, would break this.

Pinned by: `TestASegmentIsNeverRewrittenByTheNextCompaction` (session), `TestSegmentBytesAreStableAndEveryInstanceBuildsTheSameRequest` (modelctx), `TestRequestOnlyGrowsAtItsEndAcrossCompactions` (internal/workload).

### 2. A Segment and the Checkpoint that lists it are written together

`Record.CloseSegmentAndCheckpoint` queues both Entries under one lock, as neighbours of one batch, so no stored Record holds the one without the other. The Checkpoint refuses a number that is not closed, repeated or out of order. A Segment that ends up listed by nothing is harmless: a load reads it, leaves it out of the Context, and never gives its number to another Segment.

Pinned by: `TestCloseSegmentAndCheckpointWritesBothTogether`, `TestCheckpointSegmentsRefusesDuplicateOutOfOrderAndUnclosed`, `TestLoadReadsTheLargestListedSegmentNumber` (session).

### 3. The cut falls on a Turn boundary

A tool call keeps its result: the cut moves forward to a Message that is not a tool result. By default the kept tail is the latest Run's Turns, up to 20K tokens and at most a quarter of the model's window (or of the ceiling when no window is known). `KeepMessages` replaces that with the last N Messages. Mid-Run, the tail holds at least the latest Turn, whatever it weighs, because the next model call continues from it. Before a Run's first call, the Run that is starting does not count as the latest: the Run before it stays in the tail.

Pinned by: `TestKeptTailStartsAtATurnSoACallKeepsItsResult`, `TestDefaultTailIsTheLatestRunsTurnsWithinABudget`, `TestACompactionWhoseTailCoversTheStretchClosesNothing` (modelctx).

### 4. Code builds a Segment, and a missing Recap never costs it

The user's Messages and each Run's final reply stay word for word (cut at 2K tokens, the rest read with `read_segment`); each tool call is one line (arguments cut at 200 characters, repeats counted, past 100 lines counted by tool name). Tool results, reasoning, a Run's other text and Gotato's own notices are left out. The Recap is the only part a model writes. A Recap that fails, or that was never asked for, leaves the Segment with its conversation and tool call lines; the compaction goes on, and the Event says why (`recap_error`).

A Recap fails when the reply calls a tool, is cut off at the output limit, does not start with one of the five headings, uses any other `## ` heading, or has no `## What happened`. A heading with nothing under it may be left out.

Pinned by: `TestCompactBuildsASegmentFromTheConversationAndOneLinePerCall`, `TestEachFailureRuleLeavesTheSegmentWithoutARecap` (modelctx), `TestARecapThatFailsLeavesTheSegmentWithoutOne`.

### 5. A Turn compacts at most once

Whatever the trigger, and a compaction that was dropped counts. Still past the ceiling afterwards, the Run ends with `limit_exceeded`; still refused as too long, with `context_overflow`. The first guard against both is `MaxToolResultBytes`, since one oversized tool result is the usual cause.

Pinned by: `TestATurnThatCompactedPastTheCeilingIsNotCompactedAgainOnAnOverflow`, `TestASecondRefusalEndsTheRunWithContextOverflow`, `TestARunStillPastTheCeilingAfterItsCompactionEndsWithLimitExceeded` (modelctx).

### 6. The Segments stay bounded, and nothing they held is lost

The Segments together have a budget: half the ceiling, or 32K tokens with no ceiling. When they pass it, the oldest merge, down to half the budget and at least two Segments, into one merged Segment (`<segment n="1-3">`) that always opens the Context. Only that far end is a summary of a summary. The Segments it stands for stay in the log and are read by their own numbers with `read_segment`. A merge that fails leaves the Segments unmerged.

Pinned by: `TestTheOldestSegmentsMergeDownToHalfTheBudget`, `TestTheSegmentsBudgetIsHalfTheCeilingOrADefault`, `TestAMergeWhoseRecapFailsMergesNothing`, `TestAMergedSegmentsOriginalsStayReadableByNumber`.

## The steps of one compaction

A compaction runs in this order. Each step names what is written, so the failure section below can say what survives:

```text
 1  [min_tokens]   a stretch below the minimum answers replaced=false, reason=below_min_tokens
 2  cut            choose the kept tail; nothing before it answers reason=nothing_to_close
 3  compaction_started
 4  Recap          ask the writer; a failure only blanks the Recap
 5  build          code renders the Segment; the tools to keep loaded are chosen
 6  write          segment_closed + compaction_ended, one batch      (the only step that can drop it)
 7  tool_unloaded  one Event for each tool not kept
 8  merge          if the Segments now pass their budget (own Checkpoint, own Recap)
```

A compaction must not run while a Run is using the Session; the automatic one runs at the hook described below, and the others take the Session's Claim first.

## The Recap request

The Recap is asked of the Agent's own model, on the prefix it already has cached. The request is the Context the next model call would send, plus one plain user message at the end. This shows the request, from the top:

```text
system / tools / Segments / tail   exactly what the next model call would send
user (never stored, no tag)        "Write a recap of the messages after the last segment …"
```

- The ask carries no tag and is never stored; neither it nor the reply becomes a Message. Only the Recap is kept, in the Segment.
- The output limit is 8K tokens; the model call has a deadline (`ModelRecap.Timeout`, the Session's two-minute model call deadline by default).
- On an overflow the Context is not sent: the request that was just refused plus the ask would be refused again. The Segment keeps no Recap and says `recap_error: skipped_overflow`.
- `ModelRecap` needs a model and a Context; without either it is refused (`ErrNoRecapModel`, `ErrNoRecapContext`) and the Segment has no Recap. A Runner supplies both. The model's Recap is off unless the Agent's `CompactPolicy.Recap` sets it.

## Merging the oldest Segments

A merge runs right after a compaction closes a Segment, at the start of a Run when the Segments are past their budget and there is nothing to close (`past_budget`), and on an overflow. It is not tried between the Turns of a Run. This shows a merge of Segments 1 to 3 when the budget is passed:

```text
before   [Seg 1][Seg 2][Seg 3][Seg 4][Seg 5] tail
after    [Seg 1-3 · one Recap][Seg 4][Seg 5] tail       Segments 4, 5 and the tail keep their bytes
```

- **One Checkpoint of its own:** `compaction_started`, the merged Segment as a `segment_closed` Entry with the next number, and `compaction_ended`.
- **The Recap** is written from the Recaps alone, so the request is small: one user message, with no instruction, tools or Context. A writer that is not a `MergeRecapWriter` (or none) leaves a digest built by code, recorded as `digest`, which keeps no tool result.
- **An existing merged Segment** is the first to merge: its Recap goes into the new one, so there is never more than one merged Segment.
- **On an overflow** the oldest are merged too when the Segments alone pass the limit the refusal names (read through `ContextLimit()`), or the budget when it names none or a higher one. When that merge fails, the Run ends with `context_overflow` and the message says why, rather than leaving a merge that kept nothing of the far past.

## The four triggers

| Trigger | When | Recorded as |
|---|---|---|
| Idle | a warm Session has been idle for min(TTL, cache retention) less a 5 minute lead, and its stretch holds at least 20K tokens | `before_cache_expires`, or `before_unload` |
| Past the ceiling | before any model call, mid-Run included, when the size is past the ceiling | `past_ceiling` |
| Overflow | the provider refuses a request as too long, before any output | `overflow` |
| On demand | `POST /v1/sessions/{id}/compact`, `Runner.Compact` | `on_demand` |

- **Size.** The input and output tokens the last model call reported, plus an estimate (bytes ÷ 4) of what came after. After a compaction the old usage no longer counts and the size is estimated afresh, so one compaction does not set off another.
- **Ceiling.** The window less max(10% of it, 16K tokens), unless the Agent or Session sets one. With no window and none set, this trigger is off.
- **Idle.** It runs on the Session's own timer, on the Instance that holds the warm copy. The cache retention counts only when it is at least twice the lead; otherwise the TTL alone decides and the compaction runs before the Session leaves memory. The timer takes the Session's lock without waiting, finds the copy current, and skips a stretch below the minimum with no write at all.
- **Hook.** The Loop calls a `TurnPreparer` before every model call, with no Tool call open. `AutoCompactor` is one: it writes what the Run has staged, compacts, and returns `Changed`, and the Loop builds the request again from the Checkpoint, so the call sends what another Instance would build from the Record.

## Failure behavior

| Failure | What happens |
|---|---|
| overflow (the provider refuses the request as too long) | before any output: compact, merge if needed, send again, once per Turn. After some output: not retried |
| the Recap fails, is empty, or the writer panics | the Segment is kept without a Recap; `recap: none` and a `recap_error`; the usage of a call that was made is still recorded |
| the Recap's model call is stopped (Run canceled) | nothing is written; a `compaction_failed` Event; the Run settles as canceled |
| the batch write of step 6 fails | the compaction is dropped: `compaction_failed`, the Record and its loaded tools are as they were; on demand returns the error, automatic lets the Run go on |
| the merge's Recap or write fails | nothing is merged and the Segment just closed stays; on an overflow the Run ends with `context_overflow` |
| still past the ceiling after a dropped or empty compaction | the Run ends with `limit_exceeded`, and the error says whether the compaction failed, had nothing to compact, or was not enough |
| a crash between the steps | see below |
| another Instance | see below |

### A crash between the steps

The Record has no half-compaction to repair, because the Context is whatever the last Checkpoint lists:

- Crash before step 6: only `compaction_started` is stored. A load starts from the previous Checkpoint and the Context is unchanged.
- Crash after the batch: Segment and Checkpoint are both there. Tools not kept are unloaded by the Checkpoint's list, whether or not the `tool_unloaded` Events were written.
- A torn write that stores the Segment but not its Checkpoint leaves an unlisted Segment (invariant 2): harmless.
- A dropped append on a live Session ends that Writer's later writes with `ErrNotPersisted`, as for any failed write; the size is then estimated afresh from the Messages, not from usage a reply reported.

Pinned by: `TestACompactionWhoseWriteFailsIsDropped`, `TestARunGoesOnWhenItsMidRunCompactionIsDropped`, `TestARunEndsWhenItsCompactionIsDroppedAndTheContextIsStillPastTheCeiling`, `TestARunStoppedDuringItsMidRunCompactionEndsCanceled`, `TestAMergeWhoseWriteFailsLeavesTheSegmentsUnmerged` (modelctx); `TestCompactionAppendsStartedThenOneCheckpoint` (session).

### Another Instance

- **A load by another Instance** builds the same request: the Checkpoint lists the Segment numbers, the kept tail (stored whole, because dropping reasoning rewrites it) and the loaded tools. Pinned by `TestScriptCompactionUnloadsAndSegmentIsReadOnAnotherInstance` (examples/larkbot).
- **A Session running elsewhere:** a compaction on demand claims the Session first, waits up to `Liveness.Wait` and then answers busy, as a Run does (`TestCompactOfASessionRunningElsewhereAnswersBusy`, service).
- **An idle compaction** is dropped if another Instance replaced the copy (`TestIdleCompactionDropsACopyAnotherInstanceReplaced`). A message that arrives meanwhile cancels the compaction and is served first (`TestRunDuringAnIdleCompactionCancelsIt`). If the Instance goes away or evicts the Session first, the stretch waits for the next idle time or the ceiling. One that cannot be written drops the copy (`session_unloaded` with `failed`), and the next Run loads the Session again (`TestFailedIdleCompactionIsDroppedAndTheSessionStaysUsable`).

## After a compaction

- **The instruction and tool definitions** are built from the current Agent on every request, so a compaction carries neither.
- **Loaded tools** the closed Segment called, and those the kept tail calls, stay loaded; the others are unloaded (`tool_unloaded`, why `compaction`), and a tool of a ToolSet the Agent installed active never is. The tool list comes before the Segments, so this is the moment it changes.
- **Reasoning.** When the Agent's model is sent only the current Round's reasoning (`modelctx.DropsReasoning`), the kept tail loses its reasoning, because the Segment changes the prefix it was written under. Otherwise it stays.
- **Notices.** Gotato's own are left out of the Segment; a notice from your service stays word for word.
- **The record** of a compaction holds the Segment's number, the sizes before and after, the writer's name, `recap_error`, `recap_usage` and the tools kept loaded.

## Where the docs and the code differ

Found while reading the code for this document:

- FEATURES.md calls `min_tokens` "the same compaction" for the warm Session's timer and a service's own call. The code differs in one step: the idle timer checks the stretch before it resumes the Writer, so a short stretch costs no write and no Claim, while `Runner.Compact` claims the Session first and checks `min_tokens` inside `modelctx.Compact`. The answer is the same (`replaced: false`); the difference is a brief Claim on a Session that is not warm.
- FEATURES.md still carries build-order narrative ("step 1", "step 2", "Gap: none") that describes how compaction was built, not what it does; none of it is in this document.

## See also

- Decisions: [Compaction closes a Segment, before the cache goes cold](../DESIGNS.md#compaction-closes-a-segment-before-the-cache-goes-cold--2026-10-19), [A Segment is written once, and a Checkpoint points to it](../DESIGNS.md#a-segment-is-written-once-and-a-checkpoint-points-to-it--2026-10-24), [Code builds a Segment, and the model writes only the Recap](../DESIGNS.md#code-builds-a-segment-and-the-model-writes-only-the-recap-at-its-end), [The Agent's own model writes the Recap](../DESIGNS.md#the-agents-own-model-writes-the-recap-on-the-cached-prefix), [One hook runs before every model call](../DESIGNS.md#one-hook-runs-before-every-model-call--2026-10-24) (these move to `plan/decisions/` with the doc diet).
- [FEATURES.md → G-F06](../FEATURES.md#g-f06--compaction-): defaults and status.
- [DESIGNS.md → principle 3](../DESIGNS.md#3-the-context-is-a-bounded-view): the Context is a bounded view.
- [GLOSSARY.md](../GLOSSARY.md): Compaction, Checkpoint, Segment, Recap.
- Code: `modelctx/compact.go`, `modelctx/merge.go`, `modelctx/auto_compact.go`, `modelctx/recap.go`, `session/segments.go`, `service/warm.go`.
