<!-- what-we-build: draft, waits for the maintainer's approval -->
# 043. Compaction moves content out of the Context and never makes it unreadable

Decided: 2026-11-02    Status: draft, waits for the maintainer's approval

## The problem
A compaction closes a Segment that leaves out Tool Results and a Run's other text, so their facts survive only in the Recap, which is off by default ([032](032-compaction-closes-a-segment-and-never-rewrites-one.md), [033](033-code-builds-a-segment-and-the-model-writes-only-the-recap.md)). The Record keeps every original, and `read_segment` reads a whole Segment page by page, but no line of a Segment names one Tool Result or one Message, so the model cannot ask for one item and cannot tell which are worth a read. A merge makes it worse: it folds Segments into a digest, or into a Recap written from earlier Recaps alone, and many merges later a detail has no path back except reading whole Segments blind. Only a Tool Result over one page gets a reference ([040](040-a-large-tool-result-is-spilled-and-read-in-pages.md)), yet smaller results are what grew the Context in a long Run, in the measurements so far.

## The choice
Whatever leaves the Context at a compaction or a merge leaves a reference to its original, and the original stays readable by that reference. A Tool Result and a Message in a Segment, and every line of a merge's digest or Recap, carry a reference in one form, the form `read_result` already takes for a spilled result. One read tool resolves a reference to a Tool Result or a Message in the Record, page by page, from the Record, so any Instance can do it; `read_segment` stays. A merge's Recap is written from the earlier Recaps together with their references, so the model can follow a line back to the original. Reasoning gets no reference and is not read back. The Recap stays an option, and the digest carries references, so a Session that merges keeps a way back even with no Recap model.

## Not chosen
- A second, deeper compaction layer: nothing yet shows what is large, so it waits for the per-part sizes of a Turn's Context (D-035).
- Keeping the Tool Results in the Segment: the Segment grows with them and the compaction gains nothing.
- Lowering the spill page so every result is spilled: each read costs a model round, the largest part of a Run's time.
- A reference only to a whole Segment, as today: the model cannot pick the one item it needs.
- Reading reasoning back: it is provider-specific and nothing needs it.

## Consequences
A compaction stays an append and a Segment is still written once ([034](034-a-segment-is-written-once.md)); a reference is a position in the Record, so it is the same on every Instance. The cost is bytes for each reference in a Segment, and a reference into pruned Entries reads as removed. How well a detail survives many merges is not measured: a check next to G-E01 is its acceptance. Open: the shape of a reference to a Message; whether a small Tool Result gets its reference only when a compaction removes it, not before; the name of the read tool.
