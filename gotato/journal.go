package gotato

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Journal is an optional interface of a Transcript that keeps the Session's
// stored Events, numbered, as the Entries of its Record. The Session calls
// Write on its own goroutine at the end of each step, after the step's
// Messages are committed through Append: when a write fails, the Run fails
// with its error, and the Events of the step are not sent. session.Record and
// session.Writer implement it; a Session whose Transcript is not a Journal
// numbers its Events in memory.
type Journal interface {
	// Write keeps events as the next Entries, in order, and returns every
	// Event it wrote with its Number: the Events it was given, and any that
	// the Record had queued before them, such as a metadata change. An
	// Event that was not written is not returned.
	Write(ctx context.Context, events []Event) ([]Event, error)
}

// replyPartEvery and replyPartBytes bound how much of a streaming reply a
// crash can lose: a ReplyPartKept is stored every second, or as soon as this
// many bytes have streamed, whichever is first.
var (
	replyPartEvery = time.Second
	replyPartBytes = 4096
)

// Durable is an optional method of a Journal: it says whether the Events it
// keeps are stored, so they survive the process. A Journal without it is
// durable. A session.Record that is not stored is not, and a Session over it
// does not write the parts of a streaming reply, which exist so that a crash
// loses at most the last of them.
type durable interface{ Durable() bool }

// isDurable reports whether the Session's Events are stored.
func (a *Session) isDurable() bool {
	if d, ok := a.transcript.(durable); ok {
		return d.Durable()
	}
	return a.durable
}

// memoryJournal numbers the Events of a Session whose Transcript keeps none.
type memoryJournal struct {
	mu   sync.Mutex
	last uint64
}

func (j *memoryJournal) Write(_ context.Context, events []Event) ([]Event, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]Event, len(events))
	for i, event := range events {
		j.last++
		out[i] = WithNumber(event, j.last)
	}
	return out, nil
}

// journalError turns a failed write into the Run's error. A conflict and a
// store failure keep their code; anything else is a store failure.
func journalError(err error) *RuntimeError {
	var runtimeErr *RuntimeError
	if errors.As(err, &runtimeErr) && (runtimeErr.Code == CodeConflict || runtimeErr.Code == CodeStoreFailure) {
		return runtimeErr
	}
	return runtimeError(CodeStoreFailure, "Journal", "cannot write the Run's Events: "+err.Error(), err)
}
