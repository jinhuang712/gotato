# Compact your own Transcript

> [Wiki](../README.md) › [How-to](../README.md#how-to) › Compact your own Transcript

By the end of this page a Transcript that is not a `record.Record` can be compacted, and says what else it can do. You need [Tune compaction](tune-compaction.md).

## Implement a SegmentStore

A `record.SegmentStore` is what a compaction reads and writes. `*record.Record` is one, so a Session over a `store.Store` compacts with no work. Implement it for a Transcript of your own (see [Transcript and its Capabilities](#declare-what-a-transcript-can-do)) that keeps the originals a Segment refers to:

```go
type myStore struct{ /* your Transcript's state */ }

func (s *myStore) ID() string                                  { /* the Session's ID */ }
func (s *myStore) Tail() []gotato.Message                      { /* what came since the last Checkpoint, and what it kept */ }
func (s *myStore) Segments() []gotato.Segment                  { /* the Segments the Context starts with, oldest first */ }
func (s *myStore) NextSegment() int                            { /* the number the next Segment gets */ }
func (s *myStore) Position(id gotato.MessageID) (gotato.MessagePosition, bool) { /* where and when a Message was committed */ }
func (s *myStore) LoadedTools() []string                       { /* qualified IDs of the loaded Tools */ }
func (s *myStore) Emit(e gotato.Event)                         { /* publish compaction_started and the rest */ }

// One batch: write the Segment and the Checkpoint whose Context is every Segment held, then segment, then tail.
// The Tools it keeps loaded are compaction.LoadedTools. When it fails, nothing changed.
func (s *myStore) CloseSegment(why gotato.CompactionWhy, seg gotato.Segment, tail []gotato.Message, c *gotato.Compaction) (gotato.Segment, error) { /* ... */ }

// One batch: write the merged Segment and the Checkpoint whose Context is merged, the Segments after the ones
// replaced (replaces are the numbers of the first Segments), then tail.
func (s *myStore) MergeSegments(why gotato.CompactionWhy, merged gotato.Segment, replaces []int, tail []gotato.Message, c *gotato.Compaction) (gotato.Segment, error) { /* ... */ }

// then: record.AutoCompact(mine, compactor) is the TurnPreparer; record.Compact(ctx, mine, compactor, cfg) compacts once.
```

What the frame still guarantees: only it calls the two writes, one batch per call. `CloseSegment` lists every Segment of the Context itself (a caller cannot drop or move one) and sets the loaded Tools from `Compaction.LoadedTools` in the same step. A panic in your store is a dropped compaction, not a dead Run, and an error you return keeps its own code (a conflict stays a conflict).

## Declare what a Transcript can do

A `Transcript` is what `gotato.WithTranscript` takes, and it says what else it can do in one method, `Capabilities() gotato.Capabilities`:

| Field | Says |
|---|---|
| `ID` | the Session's ID, when not empty |
| `Journal` | writes the Events of a step as numbered Entries; nil means the Session numbers its Events in memory and stores none |
| `Durable` | the Journal's Events are stored, so they survive the process |
| `Spill` | the Journal keeps the Entries it writes, so a large Tool Result may be spilled and read back |
| `Events` | an `EventSource`: a Subscription that falls behind reads Events again from it |
| `Loaded` | a `LoadedToolSource`: the Tools a Run loaded are restored |

`record.Record` and `store.Writer` declare all of them, so you meet this only if you write a Transcript that is not backed by `store.Store`. A wrapper that embeds the concrete type forwards `Capabilities`; any other wrapper writes its own declaration, so one that forgets a part says so in code. `NewSession` refuses a Transcript that declares `Durable` without a `Journal`, `Events` and `Loaded`, so a wrapper that hides the Journal fails at construction, not weeks later as a Session that stores less. A Transcript that stores nothing declares `Capabilities{}`. For your own database, write a Store instead.

## See also

- [How compaction works](../../specs/compaction.md)
- [Write a store adapter, for a database of your own](write-a-store-adapter.md)
