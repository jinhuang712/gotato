# 046. Compaction and read-back are a fixed frame that keeps the guarantees, around parts a user can replace

Decided: 2026-11-02    Status: current

## The problem
Compaction is one pipeline over `*session.Record`: when, where to cut, how Segments and merges are built, the writes and the Events. Only the Recap writer can be swapped, and a user's own Transcript gets no compaction. Read-back is split wrongly: the core holds `SpillPageBytes` and `ReadResultToolID` and names `read_result` in every preview, installed or not; the ref form hides in `internal/text`; the service always installs its own read tools. [Principle 10](../../DESIGNS.md#10-each-layer-does-one-job-and-each-default-can-be-replaced) wants replaceable defaults, and no part may break principle 2, [034](034-a-segment-is-written-once.md) or [043](043-compaction-moves-content-out-and-never-makes-it-unreadable.md).

## The choice
`modelctx.Compactor` is a frame that runs the parts in order and alone holds the guarantees. Parts return values; only the frame writes. Each default is today's code:
- **Store** `SegmentStore`: `ID`, `Tail`, `Segments`, `NextSegment`, `Position`, `LoadedTools`, `Emit`, `CloseSegment(why, Segment, tail, *Compaction)`, `MergeSegments(why, Segment, replaces []int, tail, *Compaction)`. `*session.Record` implements it; so can any Transcript.
- **Trigger** `Due(ctx, TurnState) (CompactionWhy, bool)`. Default `CeilingTrigger`: past the ceiling, an overflow, or Segments past budget on a Run's first Turn.
- **Cut** `Cut(stretch []gotato.Message, CutInfo) int`, where the tail starts. Default `TailBudget` (`KeepMessages`, `TailTokens`).
- **Builder** `Build(closed, SegmentInfo) (Segment, error)`. Default `CodeSegments{SegmentSettings}` ([033](033-code-builds-a-segment-and-the-model-writes-only-the-recap.md), with 043's refs).
- **Recap** `RecapWriter` and `MergeRecapWriter`, no default. `WithAgentModel(gotato.Model) RecapWriter` gives a writer the Agent's model; `Validate() error` checks one.
- **Merge** `Pick(segments, budgetTokens) int`, how many oldest. Default `OldestToHalf`; its Recap is a `MergeRecapWriter`'s or the code digest.

`CompactPolicy` keeps only numbers. The frame keeps, whatever a part returns:
- **Writes:** one batch per store call; a failing or panicking part drops the compaction, not the Run.
- **Principle 2:** the store lists every Segment in the Context, so none is dropped or moved; the tail is `stretch[cut:]` as committed, less reasoning and usage; the cut moves to a Turn boundary and keeps a Run's last Turn; a merge replaces two or more of the first Segments.
- **034 and 043:** the frame stamps `Number`, `From`, `To`, `Source`, `Merged` and Message IDs, appends the Recap escaped, and refuses a tagged Part whose text holds a tag, and a ref naming nothing closed. Every item stays readable by `read_segment(n)`.
- **Per Turn:** once per Turn; flush before writing, hard ceiling after ([038](038-a-limit-guards-a-real-failure.md)).

Read-back:
- **Ref form** is a contract, since Segments carry it forever: public package `ref` (`Result`, `Message`, `Parse`, `Safe`, IDs `ReadResult`, `ReadSegment`).
- **Page size** has one owner, `gotato.WithReadBack(gotato.ReadBack)`, a `ToolSource` with `PageBytes() int`. The spill threshold, the preview and both read tools use it.
- **Spill** stays in core, only with a `ReadBack` installed, so every ref has a reader. `CoreLimits.SpillPreviewBytes` sets the preview; the core writes the ref line.
- **Originals** `Ref(ctx, ref.Ref) (gotato.Message, error)` and `Segment(ctx, n int) ([]gotato.Message, bool, error)`. Default `modelctx.ReadBack{Originals: RecordOriginals(record, log)}`. IDs, paging, escaping, the read Effect and wording stay fixed.

## Not chosen
- More fields on one pipeline: a user's Transcript still gets nothing.
- A part that writes: it could rewrite a Segment or skip its Checkpoint.
- A seam for the token estimate: bytes over 4 is the one rule everywhere.
- Renaming or disabling `read_result` and `read_segment`: written Segments name them.
- Moving the four tags out of core: `ForModel` escapes them in every request.
- `Originals` on `service.AgentProfile`: `session.Records` is that seam.
- Matching `ModelRecap` by type: a user's writer misses the model, and `*ModelRecap` slips past.

## Consequences
One step is replaced without copying the rest, and any Transcript compacts. The cost is seven interfaces stable from 0.3.0. It departs from [009](009-one-loop-extended-at-fixed-stages.md)'s "concrete until a second exists": the user's part is the second, and 009 should say so. A decision is not rewritten, so 009 stays as it is: read its "second implementation" for compaction as the user-replaceable parts of this decision.

1. Add `ref`; delete `gotato.ReadResultToolID`, `SpillPageBytes`, `SpillPreviewBytes`, `modelctx.ReadResultID`, `ReadSegmentID`, the ref helpers of `internal/text`.
2. Add `gotato.ReadBack`, `WithReadBack`, `CoreLimits.SpillPreviewBytes`; drop `PageBytes` from `ReadResultConfig` and `ReadSegmentConfig`. This fixes a bug: spill uses a fixed page size while `read_result` may use another.
3. `session`: `CloseSegment` lists Segments itself and sets loaded Tools from `Compaction.LoadedTools`.
4. `modelctx`: `Compactor`, parts, defaults; `Compact` and `AutoCompact` take a `SegmentStore`; delete `MergeConfig`, `closeSegment`, `mergeSegments` (tests use a fake store).
5. `modelctx`: `Originals`, `RecordOriginals`, `ReadBack` offering both tools.
6. `service`: the run policy's `Compact` ([044](044-an-agent-profile-and-a-task-spec-are-registered-inline-or-from-a-store.md)) becomes a `Compactor`; `withAgentModel` calls `WithAgentModel`; `sessionTools` passes `WithReadBack` instead of adding read tools.
7. Specs, glossary, extension-points guide, FEATURES.
