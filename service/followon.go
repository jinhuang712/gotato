package service

import (
	"context"

	"github.com/jinhuang712/gotato/gotato"
)

// followOn is a Run that follows another with the messages it left: the first
// is the new Run's prompt, with the ID it was queued under, and the rest wait
// in its queues, in order.
type followOn struct {
	first gotato.Queued
	rest  []gotato.Queued
}

// dropped is the message_dropped Event of each message the Run was to take,
// with why.
func (f *followOn) dropped(session gotato.SessionID, why string) []gotato.Event {
	events := make([]gotato.Event, 0, 1+len(f.rest))
	for _, m := range append([]gotato.Queued{f.first}, f.rest...) {
		events = append(events, gotato.MessageDropped{Header: gotato.NewHeader(session, "", 0), MessageID: m.Message.ID, Source: m.Source, Why: why})
	}
	return events
}

// followOn runs the Runs that follow a settled one, one after the other, while
// messages are left to run, and then ends the admitted Run. It is called on
// the goroutine of the Run that left them, whose caller has its own result
// already: the following Runs are seen in the Session's stream, in the
// Instance's stream and in the Record, not in a return value. They hold the
// Session lock like a Run that waits for it, so the one-Run-per-Session rule
// holds, and they count as the admitted Run until the last one ends, so a
// drain waits for them.
func (r *Runner) followOn(ctx context.Context, handle *runHandle, next []gotato.Queued, release func()) {
	// The caller's context ends with its request; the following Runs outlive
	// it, and stop through the handle as any Run does.
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	r.rearm(handle, cancel)
	go func() {
		defer release()
		defer r.unregisterHandle(handle)
		defer cancel()
		for len(next) > 0 {
			first := next[0]
			// The messages came after the Run's own request, so its Timeout
			// and Agent override are not theirs: the Agent's RunTimeout bounds
			// the Run, and its Agent is the one the Record names.
			request := RunRequest{SessionID: handle.sessionID, Prompt: gotato.TextOf(first.Message)}
			_, _ = r.serve(runCtx, runCtx, request, nil, handle, &followOn{first: first, rest: next[1:]})
			next = handle.takeNext()
			if len(next) > 0 {
				r.rearm(handle, cancel)
			}
		}
	}()
}

// rearm makes the handle the admitted Run's again for the Run that follows: no
// RunID yet and no way to stop it yet, and the stop of the settled Run cleared.
func (r *Runner) rearm(handle *runHandle, cancel context.CancelFunc) {
	handle.mu.Lock()
	handle.runID, handle.abort, handle.cancel = "", nil, cancel
	// A stop that reached the settled Run is over with it; one that came after
	// the Run settled is for this one.
	handle.stopped, handle.stopNext, handle.settled = handle.stopNext, false, false
	handle.mu.Unlock()
}
