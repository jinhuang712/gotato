package gotato

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Journal is an optional interface of a Transcript that keeps the Session's
// stored Events, numbered, as the Entries of its Record. The Session calls
// Write under its write lock, which numbers the Events in order, so Writes do
// not overlap, but from any goroutine: the Session's own at the end of each
// step, after the step's Messages are committed through Append, and the
// goroutine of a Steer, a Notice or an Abort for an Event of its own. When a
// step's write fails, the Run fails
// with its error, and the Events of the step are not sent. session.Record and
// session.Writer implement it; a Session whose Transcript is not a Journal
// numbers its Events in memory.
// [SEAM] a store other than session.Record plugs in here; no Gotato package implements it besides session.
type Journal interface {
	// Write keeps events as the next Entries, in order, and returns every
	// Event it wrote with its Number: the Events it was given, and any that
	// the Record had queued before them, such as a metadata change. An
	// Event that was not written is not returned.
	//
	// Write may be called with a context that is already done: the Run's
	// last writes (run_ended, the closing results, abort_requested) are
	// made under a context detached from the caller's cancellation and
	// bounded by a timeout, so a Journal must still write then.
	Write(ctx context.Context, events []Event) ([]Event, error)
}

// finalWriteTimeout bounds the Run's last writes, which are made under a
// context detached from the caller's cancellation.
const finalWriteTimeout = 10 * time.Second

// finalContext returns a context for a write that must happen although ctx
// ended: it keeps ctx's values, drops its cancellation, and is bounded.
func finalContext(ctx context.Context) (context.Context, context.CancelFunc) {
	// [DATA-LOSS] the Run's last writes still happen when its caller canceled, and a stalled Journal cannot hold them for good.
	return context.WithTimeout(context.WithoutCancel(ctx), finalWriteTimeout)
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
// store failure keep their code; a Session that was deleted under the Run is
// not_found, an error of a Run that ran, which no retry can cure; anything
// else is a store failure.
func journalError(err error) *RuntimeError {
	var runtimeErr *RuntimeError
	if errors.As(err, &runtimeErr) && (runtimeErr.Code == CodeConflict || runtimeErr.Code == CodeStoreFailure) {
		return runtimeErr
	}
	if errors.As(err, &runtimeErr) && runtimeErr.Code == CodeNotFound {
		gone := runtimeError(CodeNotFound, "Journal", "the Session was deleted while the Run was going on", err)
		gone.NotRun, gone.Retryable = false, false
		return gone
	}
	// [SECURITY] a Journal's error can carry a DSN, a path or a secret, and Message reaches clients.
	return runtimeError(CodeStoreFailure, "Journal", PublicText(err, "cannot write the Run's Events"), err)
}
