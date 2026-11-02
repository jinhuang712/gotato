package record

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/ref"
)

// Originals is where the originals of what compaction and the spill moved out
// of the Context are read from: the part of ReadBack that knows the store.
// ReadBack owns the IDs of the tools, the paging, the escaping, the read
// Effect and the wording; an Originals only finds Messages.
//
// OriginalsOf reads a record.Record and its log. Any Transcript that
// keeps the originals can supply its own.
// [SEAM] a Transcript that is not a record.Record says where its originals are.
type Originals interface {
	// Ref returns the original a ref names, whole: a Tool Result Message with
	// the full text of its body, even a spilled one, or a user or assistant
	// Message. It fails with ErrNoOriginal when nothing in the conversation
	// has the ref, and ErrRemoved when it had one that was pruned. Any other
	// error is a store failure: its text is never shown to the model. The zero
	// Ref asks what there is to read: it fails with ErrNoOriginal, and the
	// error's Hint() string, if it has one, is shown to the model after the
	// words of a ref that names nothing.
	Ref(ctx context.Context, r ref.Ref) (gotato.Message, error)
	// Segment returns the Messages the Segment n closed, in order, and
	// whether it returned them all: false when the store pruned the first
	// ones. It returns no Messages, with a nil error or ErrRemoved, when it
	// pruned them all, and ErrNoOriginal for a number no Segment has.
	Segment(ctx context.Context, n int) (messages []gotato.Message, complete bool, err error)
}

var (
	// ErrNoOriginal is what Originals returns for a ref or a Segment number
	// that nothing in the conversation has.
	ErrNoOriginal = errors.New("record: nothing in the conversation has that ref")
	// ErrRemoved is what Originals returns for an original that the store has
	// pruned: only what the conversation keeps of it is left.
	ErrRemoved = errors.New("record: the original was removed from the store")
)

// noOriginal is an ErrNoOriginal that adds what a model can read instead, in
// text for the model, such as the refs of the results the Record holds.
type noOriginal struct{ hint string }

func (e noOriginal) Error() string        { return ErrNoOriginal.Error() }
func (e noOriginal) Is(target error) bool { return target == ErrNoOriginal }
func (e noOriginal) Hint() string         { return e.hint }

// OriginalsOf is the Originals of a record.Record: a Tool Result that was
// spilled is read from the live Record's cache or else from log, by the
// position its Message records, and any other Message or Tool Result from the
// Context, and then from the Entries each Segment closed over, newest Segment
// first. log is the Store of the Record. It also lists the Record's Segments
// (Segments), so ReadBack offers read_segment only once it has one.
func OriginalsOf(rec *Record, log EntryLog) Originals {
	return recordOriginals{rec: rec, log: log}
}

type recordOriginals struct {
	rec *Record
	log EntryLog
}

// Segments lists the Segments of the Record's Context.
func (o recordOriginals) Segments() []gotato.Segment { return o.rec.Segments() }

// Ref implements Originals.
func (o recordOriginals) Ref(ctx context.Context, r ref.Ref) (gotato.Message, error) {
	switch r.Kind {
	case ref.KindMessage:
		id := gotato.MessageID(r.ID)
		return o.find(ctx, func(m gotato.Message) bool {
			return m.ID == id && m.ToolResult == nil && (m.Role == gotato.RoleUser || m.Role == gotato.RoleAssistant)
		})
	case ref.KindResult:
		return o.result(ctx, gotato.ToolCallID(r.ID))
	}
	return gotato.Message{}, noOriginal{hint: o.listRefs()}
}

func (o recordOriginals) result(ctx context.Context, call gotato.ToolCallID) (gotato.Message, error) {
	_, byCall := o.rec.SpilledResults()
	spilled, found := byCall[call]
	if !found && len(o.rec.Segments()) > 0 {
		// The Context no longer holds the Message of a call that a Segment
		// closed over; its Entry is still in the log.
		var err error
		spilled, found, err = o.rec.FindSpilled(ctx, o.log, call)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return gotato.Message{}, err
		}
	}
	if !found {
		// A result under one page is not spilled: it is read from the Record
		// like a message.
		return o.find(ctx, func(m gotato.Message) bool { return m.ToolResult != nil && m.ToolResult.CallID == call })
	}
	body, pruned, err := o.rec.SpilledBody(ctx, o.log, spilled)
	if errors.Is(err, ErrNotFound) {
		pruned, err = true, nil
	}
	if err != nil {
		return gotato.Message{}, err
	}
	if pruned {
		return gotato.Message{}, ErrRemoved
	}
	return gotato.Message{
		Role:       gotato.RoleToolResult,
		ToolResult: &gotato.ToolResult{CallID: call, Status: gotato.ToolResultStatusOK, Content: []gotato.ContentPart{{Kind: gotato.ContentKindText, Text: body}}},
	}, nil
}

// find looks for the latest Message that match accepts: in the Context, which
// holds the tail, and then in the Entries of each Segment
// (record.CommittedMessages), newest first. A Message that is in none of
// them is ErrRemoved when some of those Entries are gone or were not recorded,
// so it may have been in them, and ErrNoOriginal otherwise.
func (o recordOriginals) find(ctx context.Context, match func(gotato.Message) bool) (gotato.Message, error) {
	for _, message := range slices.Backward(o.rec.Messages()) {
		if match(message) {
			return message, nil
		}
	}
	gap := false
	refs, _ := refsOf(o.rec.Segments())
	for _, at := range slices.Backward(refs) {
		messages, complete, same, err := o.committed(ctx, at)
		if err != nil {
			return gotato.Message{}, err
		}
		gap = gap || !same || !complete
		for _, message := range slices.Backward(messages) {
			if match(message) {
				return message, nil
			}
		}
	}
	if gap {
		return gotato.Message{}, ErrRemoved
	}
	return gotato.Message{}, noOriginal{hint: o.listRefs()}
}

// committed reads the Messages of one Segment's range from the log. same is
// false when the Record was deleted and created again under the same ID, which
// is not the history a fork inherited.
// [DATA-LOSS] a Record deleted and created again under the same ID must not be read as the history a fork inherited.
func (o recordOriginals) committed(ctx context.Context, at segmentRef) (messages []gotato.Message, complete, same bool, err error) {
	id := o.rec.ID()
	if at.source.SessionID != "" {
		id = string(at.source.SessionID)
	}
	same, err = SameRecord(ctx, o.log, id, at.source.RecordEpoch)
	if err != nil || !same {
		return nil, false, same, err
	}
	messages, complete, err = CommittedMessages(ctx, o.log, id, at.source.FromNumber, at.source.ToNumber)
	return messages, complete, true, err
}

// Segment implements Originals.
func (o recordOriginals) Segment(ctx context.Context, n int) ([]gotato.Message, bool, error) {
	refs, _ := refsOf(o.rec.Segments())
	at := slices.IndexFunc(refs, func(r segmentRef) bool { return r.number == n })
	if at < 0 {
		return nil, false, noOriginal{}
	}
	messages, complete, _, err := o.committed(ctx, refs[at])
	return messages, complete, err
}

// listRefs says which results the Context holds, for an error that names a
// ref that is not one.
func (o recordOriginals) listRefs() string {
	calls, _ := o.rec.SpilledResults()
	if len(calls) == 0 {
		return "; this conversation has no spilled result"
	}
	refs := make([]string, len(calls))
	for i, call := range calls {
		refs[i] = ref.Result(string(call)).String()
	}
	return "; the results you can read are " + strings.Join(refs, ", ")
}
