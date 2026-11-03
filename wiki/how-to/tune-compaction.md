# Tune compaction

> [Wiki](../README.md) › [How-to](../README.md#how-to) › Tune compaction

By the end of this page you have changed when a Session compacts, where the kept tail starts, how a Segment reads, or who writes the Recap, and left the rest as it was.

## Have the model write the Recap

A Recap is the end of a Segment, written when a Session compacts. With no writer, a Segment has none. `record.ModelRecap` has the Agent's own model write it on the cached prefix; `TruncateRecap` is the deterministic one for tests. Set it on the Agent's `Compactor`:

```go
Compact: record.Compactor{Recap: record.ModelRecap{}}, // a Runner fills in the Agent's model
```

A writer of your own has four methods: `Name`, `WriteRecap`, `WithAgentModel(gotato.Model) RecapWriter` (the Runner calls it to hand over the Agent's model; a writer that asks no model returns itself, and one that wraps a `ModelRecap` passes the model on) and `Validate() error` (a misconfiguration is an error at first use, not a Segment that quietly has none). Embedding the writer you wrap gives you the methods you do not change:

```go
// loggedRecap logs each Recap it is asked for and leaves the writing to the writer it wraps.
type loggedRecap struct{ record.RecapWriter }

func (l loggedRecap) WriteRecap(ctx context.Context, req record.RecapRequest) (gotato.Message, error) {
	log.Printf("recap for segment %d", req.Segment)
	return l.RecapWriter.WriteRecap(ctx, req)
}

// The Runner calls this with the Agent's model, so the wrapped writer must get it.
func (l loggedRecap) WithAgentModel(m gotato.Model) record.RecapWriter {
	return loggedRecap{l.RecapWriter.WithAgentModel(m)}
}

// in profile.AgentProfile: Compact: record.Compactor{Recap: loggedRecap{record.ModelRecap{}}},
```

Embedding the interface hides `WriteMergeRecap`, so merges of this writer get the digest built by code; add the method to write them too. The frame still appends your Recap escaped, so it cannot end its tag early, and a writer that fails leaves the Segment without a Recap, not without a compaction.

## Choose when to compact and where to cut

Compaction is a frame, `record.Compactor`, around parts. You replace a part and keep the rest; a nil part is its default, so `record.Compactor{}` is how compaction behaved before the parts were named. `CompactPolicy` (embedded) holds only numbers: `CeilingTokens`, `WindowTokens`, `DropReasoning`, `SegmentBudgetTokens` and `Location`. [How the frame works](../../specs/compaction.md).

```go
Compact: record.Compactor{
	CompactPolicy: record.CompactPolicy{WindowTokens: 200_000},
	Trigger:       everyTwentyTurns{},
	Cut:           keepLastTwenty{},
},
```

A `Trigger` says when a compaction is due, and why. It is asked once before each model call; `gotato.CompactionWhyPastBudget` means only the oldest Segments are due to merge, any other reason closes a Segment. Wrap `record.CeilingTrigger` to add a rule and keep the default ones (overflow, the ceiling, Segments past budget):

```go
// everyTwentyTurns adds a rule to the default Trigger: also compact on every 20th Turn of a Run.
type everyTwentyTurns struct{ record.CeilingTrigger }

func (t everyTwentyTurns) Due(ctx context.Context, s record.TurnState) (gotato.CompactionWhy, bool) {
	if why, due := t.CeilingTrigger.Due(ctx, s); due {
		return why, true
	}
	if s.Turn%20 == 0 {
		return gotato.CompactionWhyOnDemand, true
	}
	return "", false
}
```

A `Cutter` says where the kept tail starts, as an index into the Messages since the last Checkpoint; the Messages before it close into the Segment, and 0 closes nothing:

```go
// keepLastTwenty keeps the last 20 Messages word for word.
type keepLastTwenty struct{}

func (keepLastTwenty) Cut(stretch []gotato.Message, _ record.CutInfo) int {
	return max(0, len(stretch)-20)
}
```

What the frame still guarantees: a Turn compacts at most once, whatever the Trigger says, and a Run still past the ceiling after its compaction ends `limit_exceeded`; a Trigger that panics, or says due with no reason, drops that Turn's compaction and publishes an `extension_failed` Event. The cut must be inside the stretch (else the compaction fails); the frame moves it forward to a Turn boundary so a tool call keeps its result, and back so a Run's last Turn is kept, and the tail is the Messages as committed, less reasoning and usage. A Cutter cannot drop or move a Segment.

For one call, `record.CompactConfig` changes the numbers, `KeepMessages` (the Cutter becomes `TailBudget{KeepMessages}`) and the Recap writer; it does not need a part of its own.

## Change how a Segment reads and how many merge

A `Builder` renders the closed Messages as a Segment. Its settings live with it: `CodeSegments{SegmentSettings{MessageTokens, ToolLines, ArgChars}}`, so most teams change a number and write no code:

```go
Builder: record.CodeSegments{SegmentSettings: record.SegmentSettings{MessageTokens: 500}},
```

A Builder of your own wraps the default or writes the Messages itself:

```go
// countedSegments builds the default Segment and counts it.
type countedSegments struct {
	record.CodeSegments
	built *atomic.Int64
}

func (b countedSegments) Build(closed []gotato.Message, info record.SegmentInfo) (gotato.Segment, error) {
	b.built.Add(1)
	return b.CodeSegments.Build(closed, info)
}
```

What the frame still guarantees: it stamps `Number`, `From`, `To`, `Source`, `Merged` and the Message IDs `segment-<n>-<i>`, appends the Recap escaped, and refuses what would break decisions [034: A Segment is written once](../../plan/decisions/034-a-segment-is-written-once.md) and [043: Moved content stays readable](../../plan/decisions/043-compaction-moves-content-out-and-never-makes-it-unreadable.md): a Message that is not a plain user or assistant Message, a tagged Part (one of the four tags) whose text holds a tag or is not of the form `<tag ...>...</tag>`, and a ref that names nothing the Segment closed. The refs it checks are the two shapes `CodeSegments` writes: `[result:<call_id> ...]` ending a tool call line in `<tool_calls>`, and a line `ref msg:<message_id>` in `<segment>` (build them with package `ref`). A Builder that errs or panics drops the compaction.

A `Picker` says how many of the oldest Segments a merge replaces. It is asked only when the Segments pass their budget and there are at least two:

```go
// allButTwo folds every Segment but the newest two, once there are at least two to fold.
type allButTwo struct{}

func (allButTwo) Pick(segments []gotato.Segment, _ int) int {
	if n := len(segments) - 2; n >= 2 {
		return n
	}
	return 0 // merges nothing
}
```

The frame replaces two or more of the first Segments, never a later one. An answer of 1, or of more than there are, fails the merge with the reason `pick_failed` and nothing is merged; 0 merges nothing.

## Write the Recap of merged Segments

When the oldest Segments pass their budget they are merged into one (the Picker says how many). A `RecapWriter` that also has `WriteMergeRecap` writes the Recap of the merge (`ModelRecap` does). Any other writer leaves the merged Segment with a digest built by code.

## See also

- [How compaction works](../../specs/compaction.md)
- [Compaction, as the user sees it](../../FEATURES.md#f5-compaction)
- [Compact your own Transcript](compact-your-own-transcript.md)
