package modelctx

import (
	"context"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// The parts of a compaction. A Compactor is a frame that runs them in order
// and alone holds the guarantees; a part returns a value, and only the frame
// writes. Each default is the code that ran before the parts were named, and
// a user replaces one without copying the rest. Whatever a part returns, the
// frame keeps decision 046's rules: see Compactor.

// SegmentStore is what a compaction reads from and writes to: the Context's
// Segments and tail, and the two batches that close a Segment and merge the
// oldest ones. *session.Record implements it, and so can any Transcript that
// keeps the originals a Segment refers to.
//
// Reads return what the Context holds now. A write is one batch: it closes or
// merges and writes the Checkpoint that lists the result together, and when it
// fails, nothing changed.
// [SEAM] a Transcript that is not a session.Record compacts through its own store.
type SegmentStore interface {
	// ID is the Session's ID, which the Events of a compaction carry.
	ID() string
	// Tail returns the Messages after the Segments: everything committed since
	// the last Checkpoint and what that Checkpoint kept.
	Tail() []gotato.Message
	// Segments returns the Segments the Context starts with, oldest first.
	Segments() []gotato.Segment
	// NextSegment is the number the next closed Segment gets.
	NextSegment() int
	// Position says where and when a Message was committed since the last
	// Checkpoint; ok is false for one it has no position for.
	Position(gotato.MessageID) (gotato.MessagePosition, bool)
	// LoadedTools returns the qualified IDs of the Tools the model loaded.
	LoadedTools() []string
	// Emit publishes an Event about the Session (compaction_started and the
	// rest).
	Emit(gotato.Event)
	// CloseSegment closes segment and, in the same batch, writes the
	// Checkpoint whose Context is every Segment the Context holds, then
	// segment, then tail. The loaded Tools it keeps are compaction.LoadedTools.
	CloseSegment(why gotato.CompactionWhy, segment gotato.Segment, tail []gotato.Message, compaction *gotato.Compaction) (gotato.Segment, error)
	// MergeSegments closes merged, which stands for the Segments it names in
	// Merged, and in the same batch writes the Checkpoint whose Context is
	// merged, then the Segments after the replaced ones, then tail. replaces
	// are the numbers of the Context's first Segments that merged takes the
	// place of.
	MergeSegments(why gotato.CompactionWhy, merged gotato.Segment, replaces []int, tail []gotato.Message, compaction *gotato.Compaction) (gotato.Segment, error)
}

// TurnState is what a Trigger sees before a model call.
type TurnState struct {
	Run  gotato.RunID
	Turn gotato.TurnNumber
	// Overflow is true when the provider refused the request as too long: the
	// one chance of the Turn to shorten it.
	Overflow bool
	// Tokens is the size of the Context the next call sends: the input and
	// output the last reply reported plus an estimate of what came after it,
	// or an estimate of everything and of the instruction and the tools.
	Tokens int
	// CeilingTokens is the ceiling in force, 0 for none.
	CeilingTokens int
	// SegmentTokens is what the Segments hold, estimated, and BudgetTokens the
	// most they may hold before the oldest merge. SegmentTokens is set on a
	// Run's first Turn and is 0 on the others.
	SegmentTokens int
	BudgetTokens  int
}

// Trigger says when a compaction is due. Due is asked once before each model
// call, and answers why: gotato.CompactionWhyPastBudget means only the oldest
// Segments are due to merge, any other reason that a Segment closes (and the
// Segments merge after it, if they pass their budget). The frame compacts at
// most once per Turn, whatever the answer, and ends a Run that is still past
// the ceiling after its compaction. CeilingTrigger is the default.
// [SEAM] a host decides when a Session compacts.
type Trigger interface {
	Due(ctx context.Context, state TurnState) (why gotato.CompactionWhy, due bool)
}

// CutInfo is what a Cutter knows besides the Messages.
type CutInfo struct {
	// WindowTokens is the Model's window, 0 when unknown, and CeilingTokens
	// the ceiling in force, 0 for none.
	WindowTokens  int
	CeilingTokens int
	// CurrentRun is the Run that is starting, 0 when none is: its Messages are
	// not "the latest Run" when a tail is chosen.
	CurrentRun gotato.RunID
}

// Cutter says where the kept tail starts. Cut returns the index in stretch,
// the Messages since the last Checkpoint, of the first Message the tail
// keeps: the Messages before it are closed into a Segment. 0 closes nothing.
// The frame moves the index forward to a Turn boundary, so a tool call keeps
// its result, and back to keep a Run's last Turn; an index outside the stretch
// fails the compaction. TailBudget is the default.
// [SEAM] a host decides how much of the recent conversation stays word for word.
type Cutter interface {
	Cut(stretch []gotato.Message, info CutInfo) int
}

// SegmentInfo is what a Builder needs besides the closed Messages.
type SegmentInfo struct {
	// Number is the number the Segment will have.
	Number int
	// From and To are when the closed stretch began and ended, zero when
	// unknown. The frame stamps them on the Segment.
	From, To time.Time
	// Location is the time zone the Segment shows its times in.
	Location *time.Location
	// Recap is the text of the Recap that ends the Segment, empty when there is
	// none. The frame appends it, escaped, after what the Builder returns, so a
	// Builder only words its header by it.
	Recap string
	// Position says when a Message was committed.
	Position func(gotato.MessageID) (gotato.MessagePosition, bool)
}

// Builder renders the closed Messages as a Segment: the Messages the Context
// holds in their place and the line of each tool call. The frame stamps
// Number, From, To, Source and Merged, the Message IDs and the loaded Tools,
// appends the Recap, and refuses a Segment that breaks the rules of 034 and
// 043: a tagged Part whose text holds a tag, a ref (the form of package ref)
// that names nothing the Segment closes, or a Message that is not a plain user
// or assistant Message. CodeSegments is the default.
//
// A ref the frame checks is one in the two shapes CodeSegments writes: the
// `[result:<call_id> ...]` that ends a tool call line inside <tool_calls>, and
// a line `ref msg:<message_id>` inside <segment>.
// [SEAM] a host decides what a closed stretch keeps.
type Builder interface {
	Build(closed []gotato.Message, info SegmentInfo) (gotato.Segment, error)
}

// Picker says how many of the oldest Segments a merge replaces. Pick is asked
// only when the Segments together pass budgetTokens and there are at least two,
// and returns how many of the first Segments to merge: two or more, at most all
// of them. 0 merges nothing; anything else fails the merge. The merged
// Segment's Recap is a MergeRecapWriter's, or the digest built by code.
// OldestToHalf is the default.
// [SEAM] a host decides how much of the far past is folded at once.
type Picker interface {
	Pick(segments []gotato.Segment, budgetTokens int) int
}
