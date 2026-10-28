# Compaction

How a Session keeps the model's input inside its window. Defaults are in [FEATURES.md → G-F06](../FEATURES.md#g-f06--compaction-).

## TL;DR

- A compaction closes what came since the last Checkpoint, less a kept tail, into a Segment, and writes the Segment and a Checkpoint that lists it in one step. The Record only grows.
- Code builds the Segment. The Agent's model writes only the Recap, and a Segment without one is valid.
- When the Segments pass their budget, the oldest merge into one. A compaction may fail; the Run goes on or ends with a code that says why.

## The shape of the Context

Each compaction adds one Segment after the ones before it:

```text
before   [Segment 1][Segment 2] the Messages since the last Checkpoint …
after    [Segment 1][Segment 2][Segment 3] the kept tail
```

A merge when the budget is passed:

```text
before   [Seg 1][Seg 2][Seg 3][Seg 4][Seg 5] tail
after    [Seg 1-3 · one Recap][Seg 4][Seg 5] tail       Segments 4, 5 and the tail keep their bytes
```

## Invariants

1. A Segment is written once, as its own `segment_closed` Entry, so a provider's prompt cache can read the same bytes every time.
   Pinned by: `TestASegmentIsNeverRewrittenByTheNextCompaction` (session), `TestSegmentBytesAreStableAndEveryInstanceBuildsTheSameRequest` (modelctx), `TestRequestOnlyGrowsAtItsEndAcrossCompactions` (internal/workload).

2. A Segment and the Checkpoint that lists it are written in one batch, so no stored Record holds one without the other.
   Pinned by: `TestCloseSegmentAndCheckpointWritesBothTogether`, `TestCheckpointSegmentsRefusesDuplicateOutOfOrderAndUnclosed`, `TestLoadReadsTheLargestListedSegmentNumber` (session).

3. The cut falls on a Turn boundary, so a tool call keeps its result.
   Pinned by: `TestKeptTailStartsAtATurnSoACallKeepsItsResult`, `TestDefaultTailIsTheLatestRunsTurnsWithinABudget`, `TestACompactionWhoseTailCoversTheStretchClosesNothing` (modelctx).

4. Code builds a Segment, and a missing Recap never costs it; the Event says why (`recap_error`).
   Pinned by: `TestCompactBuildsASegmentFromTheConversationAndOneLinePerCall`, `TestEachFailureRuleLeavesTheSegmentWithoutARecap` (modelctx), `TestARecapThatFailsLeavesTheSegmentWithoutOne`.

5. A Turn compacts at most once, and a dropped compaction counts.
   Pinned by: `TestATurnThatCompactedPastTheCeilingIsNotCompactedAgainOnAnOverflow`, `TestASecondRefusalEndsTheRunWithContextOverflow`, `TestARunStillPastTheCeilingAfterItsCompactionEndsWithLimitExceeded` (modelctx).

6. The Segments stay within a budget, and what a merge folds in stays readable by number with `read_segment`.
   Pinned by: `TestTheOldestSegmentsMergeDownToHalfTheBudget`, `TestTheSegmentsBudgetIsHalfTheCeilingOrADefault`, `TestAMergeWhoseRecapFailsMergesNothing`, `TestAMergedSegmentsOriginalsStayReadableByNumber`.

## The steps of one compaction

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

## The Recap request

- It is the Context the next model call would send, plus one user message; neither the ask nor the reply is stored.
- On an overflow the Context is not sent: `recap_error: skipped_overflow`.

## The four triggers

| Trigger | Meaning | Recorded as |
|---|---|---|
| Idle | a warm Session is about to lose its cache | `before_cache_expires`, `before_unload` |
| Past the ceiling | the size passes the ceiling before a model call | `past_ceiling` |
| Overflow | the provider refused the request as too long | `overflow` |
| On demand | the service asked for it | `on_demand` |

## Failure behavior

| Failure | What happens |
|---|---|
| overflow | compact, merge if needed, send again, once per Turn |
| the Recap fails | the Segment is kept without a Recap |
| the Recap's call is canceled | nothing is written; the Run settles as canceled |
| the batch write fails | the compaction is dropped; the Record is as it was |
| the merge fails | nothing is merged; on an overflow the Run ends with `context_overflow` |
| still past the ceiling | the Run ends with `limit_exceeded` |

- A crash leaves no half-compaction: the Context is whatever the last Checkpoint lists, and an unlisted Segment is harmless. Pinned by: `TestACompactionWhoseWriteFailsIsDropped`, `TestARunGoesOnWhenItsMidRunCompactionIsDropped`, `TestARunEndsWhenItsCompactionIsDroppedAndTheContextIsStillPastTheCeiling`, `TestARunStoppedDuringItsMidRunCompactionEndsCanceled`, `TestAMergeWhoseWriteFailsLeavesTheSegmentsUnmerged` (modelctx); `TestCompactionAppendsStartedThenOneCheckpoint` (session).
- Another Instance builds the same request from the Checkpoint (`TestScriptCompactionUnloadsAndSegmentIsReadOnAnotherInstance`, examples/larkbot). On demand, a Session running elsewhere answers busy (`TestCompactOfASessionRunningElsewhereAnswersBusy`, service).
- An idle compaction is dropped if another Instance replaced the copy, and yields to a new message (`TestIdleCompactionDropsACopyAnotherInstanceReplaced`, `TestRunDuringAnIdleCompactionCancelsIt`).

## After a compaction

- Loaded tools the Segment or the tail called stay loaded; the others are unloaded.
- If the model is sent only the current Round's reasoning, the tail loses its reasoning.
- Gotato's own notices stay out of the Segment.

## See also

[FEATURES.md → G-F06](../FEATURES.md#g-f06--compaction-), [DESIGNS.md → principle 3](../DESIGNS.md#3-the-context-is-a-bounded-view), [GLOSSARY.md](../GLOSSARY.md), `modelctx/compact.go`, `modelctx/merge.go`, `session/segments.go`.
