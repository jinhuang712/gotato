# Read back from your own store

> [Wiki](../README.md) › [How-to](../README.md#how-to) › Read back from your own store

By the end of this page the model reads spilled results and old Segments from your store, at the page size you choose. You need [Tune compaction](tune-compaction.md).

## Read originals from your own store

A `gotato.ReadBack` is a `ToolSource` that also says `PageBytes() int`. It gives a Session its tools for reading back what left the Context, and the one page size that the spill threshold, the pages of a preview and both read tools share. Install it with `gotato.WithReadBack`; a Session spills a large Tool Result only with one installed (and a Transcript that keeps Entries). `record.ReadBack{Originals, PageSize, Location}` is the default and offers `read_result` and, once the Context holds a Segment, `read_segment`. A service installs it over its store, so most teams write nothing; one Agent that needs another sets `profile.AgentProfile.ReadBack`, and nil keeps the default. To read from your own store, supply `record.Originals`, and set the page size if 32 KiB (`record.DefaultPageBytes`) is not right:

```go
type myOriginals struct{ db *sql.DB }

// Ref returns a Message by its ref: result:<call_id> or msg:<message_id>.
func (o myOriginals) Ref(ctx context.Context, r ref.Ref) (gotato.Message, error) {
	// look it up; return record.ErrNoOriginal when nothing has it, record.ErrRemoved when it was pruned
	return gotato.Message{}, record.ErrNoOriginal
}

// Segment returns the Messages Segment n closed, and whether it returned all of them.
func (o myOriginals) Segment(ctx context.Context, n int) ([]gotato.Message, bool, error) {
	return nil, false, record.ErrNoOriginal
}

gotato.WithReadBack(record.ReadBack{Originals: myOriginals{db}, PageSize: 16 << 10})
```

Add `Segments() []gotato.Segment` to your `Originals`, as `record.OriginalsOf` has, and `read_segment` is offered only once the Context holds a Segment; without it, it is always offered. An `ErrNoOriginal` may carry a `Hint() string`, text shown to the model after the words for a ref that names nothing (for example the refs the store holds). Any other error is a store failure and its text is never shown to the model.

What the ReadBack still guarantees: the IDs `read_result` and `read_segment` (`ref.ReadResult`, `ref.ReadSegment`), the paging, the escaping of the four tags, the `read` Effect and the wording are fixed, since written Segments name them; only where the originals are, and how big a page is, are yours. `NewSession` asks `PageBytes` once and refuses a value below 1, or a panic, with `invalid_argument`; a Tool ID it shares with another source fails the Session like any other.

## See also

- [How large results are spilled and read](../../specs/tools.md)
- [Large results, as the user sees them](../../FEATURES.md#f67-large-results)
