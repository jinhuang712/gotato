package record

import (
	"fmt"
	"reflect"
	"runtime/debug"
	"strings"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/text"
	"github.com/jinhuang712/gotato/ref"
)

// Compactor is the frame of a compaction: it runs its parts in order and alone
// holds the guarantees. A part returns a value and never writes; the frame
// writes, one batch per store call (SegmentStore). Each nil part is its
// default, so the zero Compactor is today's behaviour, and a user replaces one
// part without copying the rest.
//
//	record.Compactor{CompactPolicy: record.CompactPolicy{WindowTokens: 200000}, Recap: record.ModelRecap{}}
//
// The parts, in the order the frame runs them: a Trigger says when a
// compaction is due, a Cutter where the kept tail starts, a Builder renders
// the closed Messages as a Segment, a RecapWriter writes the Recap that ends
// it (none by default), and a Picker says how many of the oldest Segments a
// merge replaces, whose Recap a MergeRecapWriter writes or code digests.
//
// Whatever a part returns, the frame keeps:
//   - Writes: one batch per store call. A part that fails, panics or breaks a
//     rule below drops the compaction (ErrBadPart, a compaction_failed Event),
//     not the Run. A part's argument is a copy.
//   - Principle 2: the store lists every Segment in the Context, so none is
//     dropped or moved; the tail is stretch[cut:] as committed, less
//     reasoning (DropReasoning) and usage; the cut moves to a Turn boundary and
//     keeps a Run's last Turn; a merge replaces two or more of the first
//     Segments.
//   - Decisions 034 and 043: the frame stamps Number, From, To, Source,
//     Merged and the Message IDs, appends the Recap escaped, and refuses a
//     tagged Part whose text holds a tag and a ref that names nothing the
//     Segment closed, so every item stays readable by read_segment(n).
//   - Per Turn: a Turn compacts at most once; what the Run staged is
//     flushed before the write, and a Run still past the ceiling after its
//     compaction ends with limit_exceeded (decision 038).
//
// A Compactor is a value; the parts are shared by every compaction that uses
// it, so they hold no state a compaction changes.
type Compactor struct {
	// CompactPolicy holds the numbers: the window and the ceiling, the
	// Segments' budget, DropReasoning and the time zone.
	CompactPolicy
	// Trigger says when a compaction is due; nil is CeilingTrigger.
	Trigger Trigger
	// Cut says where the kept tail starts; nil is TailBudget.
	Cut Cutter
	// Builder renders the closed Messages as a Segment; nil is CodeSegments.
	Builder Builder
	// Recap writes the Recap that ends a Segment, and, when it is also a
	// MergeRecapWriter, the Recap of a merge. Nil means the Segment has none
	// and a merge's Recap is the digest built by code. A writer that fails
	// leaves the Segment without a Recap.
	Recap RecapWriter
	// Merge says how many of the oldest Segments a merge replaces; nil is
	// OldestToHalf.
	Merge Picker
}

func (c Compactor) trigger() Trigger {
	if c.Trigger == nil {
		return CeilingTrigger{}
	}
	return c.Trigger
}

func (c Compactor) cutter() Cutter {
	if c.Cut == nil {
		return TailBudget{}
	}
	return c.Cut
}

func (c Compactor) builder() Builder {
	if c.Builder == nil {
		return CodeSegments{}
	}
	return c.Builder
}

func (c Compactor) picker() Picker {
	if c.Merge == nil {
		return OldestToHalf{}
	}
	return c.Merge
}

// Validate checks what cannot work as set: its RecapWriter (a ModelRecap needs
// a Model, which a Runner fills from the Agent's). Compact and the
// AutoCompactor call it before they do anything, so a misconfiguration is an
// error at first use, not a Segment that quietly has no Recap.
func (c Compactor) Validate() error {
	if c.Recap == nil {
		return nil
	}
	var failure error
	if err := guardPanic("RecapWriter.Validate", func() { failure = c.Recap.Validate() }); err != nil {
		return err
	}
	return failure
}

// guardPanic runs call, and turns a panic in it into an error of ErrBadPart.
// [LIVENESS] a panicking part would end the process or leave a compaction_started with no ending, instead of dropping the compaction.
func guardPanic(name string, call func()) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: %s panicked: %v\n%s", ErrBadPart, name, recovered, debug.Stack())
		}
	}()
	call()
	return nil
}

// callPart calls a part. A panic, and an error it returns, are ErrBadPart: the
// error says which part, and its own text stays in the chain.
func callPart[R any](name string, call func() (R, error)) (result R, err error) {
	if perr := guardPanic(name, func() { result, err = call() }); perr != nil {
		var zero R
		return zero, perr
	}
	if err != nil {
		var zero R
		return zero, fmt.Errorf("%w: %s failed: %w", ErrBadPart, name, err)
	}
	return result, nil
}

// callStore calls a store write. A panic is ErrBadPart; the store's own error
// is returned as it is, since its code (store_failure, conflict) is the
// caller's to read.
func callStore[R any](name string, call func() (R, error)) (result R, err error) {
	if perr := guardPanic(name, func() { result, err = call() }); perr != nil {
		var zero R
		return zero, perr
	}
	return result, err
}

// isNilStore reports whether store is nil or holds a nil pointer.
func isNilStore(store SegmentStore) bool {
	if store == nil {
		return true
	}
	v := reflect.ValueOf(store)
	return v.Kind() == reflect.Pointer && v.IsNil()
}

// checkSegment is what the frame requires of a Segment a Builder returned,
// before it stamps and writes it: the Messages are plain user and assistant
// Messages; a tagged Part is well formed and holds no tag of its own, so
// nothing in it can end its tag early and speak as the system (034); and every
// ref in the shapes CodeSegments writes names a Message or a Tool call among the
// closed Messages, so it stays readable (043).
func checkSegment(built gotato.Segment, closed []gotato.Message) error {
	if len(built.Messages) == 0 {
		return fmt.Errorf("%w: the Segment has no Messages", ErrBadPart)
	}
	for i, message := range built.Messages {
		switch {
		case message.Role != gotato.RoleUser && message.Role != gotato.RoleAssistant:
			return fmt.Errorf("%w: Message %d of the Segment has role %q, not user or assistant", ErrBadPart, i+1, message.Role)
		case len(message.ToolCalls) > 0 || message.ToolResult != nil || message.Notice != nil:
			return fmt.Errorf("%w: Message %d of the Segment holds a tool call, a tool result or a notice", ErrBadPart, i+1)
		}
		for _, part := range message.Parts {
			if tag, tagged := part.Metadata[gotato.MetaTag]; tagged {
				if err := checkTagged(tag, part); err != nil {
					return fmt.Errorf("%w: Message %d: %w", ErrBadPart, i+1, err)
				}
			}
		}
	}
	return checkRefs(built, closed)
}

// checkTagged requires a Part marked MetaTag to be one of Gotato's four tags,
// open and close, around text that holds no tag start or end: ForModel leaves
// such a Part as it is, so that text is the only guard.
func checkTagged(tag string, part gotato.ContentPart) error {
	if !isTag(tag) {
		return fmt.Errorf("a tagged Part names the tag %q, which is none of the four", tag)
	}
	closing := "</" + tag + ">"
	if part.Kind != gotato.ContentKindText || !strings.HasPrefix(part.Text, "<"+tag) || !strings.HasSuffix(part.Text, closing) {
		return fmt.Errorf("a tagged Part is not text of the form <%s ...>...%s", tag, closing)
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(part.Text, "<"), closing)
	if text.EscapeTags(inner) != inner {
		return fmt.Errorf("the text of a <%s> Part holds a tag", tag)
	}
	return nil
}

func isTag(tag string) bool {
	switch tag {
	case gotato.TagNotice, gotato.TagSegment, gotato.TagToolCalls, gotato.TagRecap:
		return true
	}
	return false
}

// checkRefs requires each ref the Segment's own parts carry to name something
// the Segment closed: a user or assistant Message by its ID, a Tool call by
// the ID of its call or of its result.
func checkRefs(built gotato.Segment, closed []gotato.Message) error {
	messages, calls := map[string]bool{}, map[string]bool{}
	for _, message := range closed {
		if message.ToolResult != nil {
			calls[string(message.ToolResult.CallID)] = true
			continue
		}
		messages[string(message.ID)] = true
		for _, call := range message.ToolCalls {
			calls[string(call.ID)] = true
		}
	}
	msgRefs, resultRefs := segmentRefs(built)
	for _, name := range append(msgRefs, resultRefs...) {
		parsed, ok := ref.Parse(name)
		known := ok && (parsed.Kind == ref.KindMessage && messages[parsed.ID] || parsed.Kind == ref.KindResult && calls[parsed.ID])
		if !known {
			return fmt.Errorf("%w: the Segment carries the ref %s, which names nothing it closed", ErrBadPart, name)
		}
	}
	return nil
}
