package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// RunRequest is one unit of work.
type RunRequest struct {
	// SessionID names the Session. Empty creates a new Session.
	SessionID string
	// Agent selects the Agent for a new Session; an existing Session
	// keeps the Agent it was created with unless Agent is set.
	Agent string
	// Prompt is the user input. It is required unless Continue is set.
	Prompt string
	// Continue resumes the loop of a Session without a new Message. It takes
	// no Prompt: a request with both is refused as invalid_argument, since the
	// Prompt would otherwise be dropped without a word.
	Continue bool
	// Metadata is applied to a new Session.
	Metadata map[string]string
	// Update sets metadata on the Session before the Run, under its Claim,
	// as the Run's first write. An empty value deletes a key.
	Update map[string]string
	// SkipSave runs without persisting the Session: a new Session lives only
	// for this Run, and a changed existing Session, Update included, is left
	// as it was.
	SkipSave bool
	// Timeout bounds this Run; the Run settles as deadline_exceeded. Zero
	// keeps the Agent's RunTimeout. It is the caller's bound for this request
	// only: a Run that follows with the messages this one left keeps the
	// Agent's RunTimeout.
	Timeout time.Duration
}

// RunResponse is the outcome of one Run plus the Session state it left.
type RunResponse struct {
	SessionID string           `json:"session_id"`
	Agent     string           `json:"agent"`
	Model     string           `json:"model,omitempty"`
	Result    gotato.RunResult `json:"result"`
	FinalText string           `json:"final_text,omitempty"`
	Compacted bool             `json:"compacted"`
	Messages  int              `json:"messages"`
	Events    int              `json:"events"`
	// RecordEpoch is the epoch of the Session's Record, for a client that
	// loses the Run's stream to resume the Session's stream with. Empty for a
	// Run that is not saved.
	RecordEpoch string `json:"record_epoch,omitempty"`
	// Reconnect is set when this Instance's drain stopped the Run before it
	// completed. By the time the Run returns, its Session is saved and
	// released, and so is every other Session of the drain.
	Reconnect *Reconnect `json:"reconnect,omitempty"`
}

// Run executes one Run synchronously.
func (r *Runner) Run(ctx context.Context, request RunRequest) (RunResponse, error) {
	return r.run(ctx, request, nil)
}

// StreamRun executes one Run and delivers every Event of its stream to sink as
// it is produced, on the caller's goroutine: the Session's stored Events and
// its deltas, from the first Event of the Run to its RunEnded. The Run itself
// runs on another goroutine, and sink reads a Subscription, so a sink that is
// slow slows only itself: the Session never waits for it, and a stored Event
// the sink falls behind on is read again from the Record. A sink error is
// advisory: it stops delivery, not the Run. StreamRun returns after the Run
// settles and its last Event has been delivered.
func (r *Runner) StreamRun(ctx context.Context, request RunRequest, sink func(gotato.Event) error) (RunResponse, error) {
	return r.StreamRunAdmitted(ctx, request, nil, sink)
}

// StreamRunAdmitted is StreamRun with a call, on the caller's goroutine, once
// the Run is admitted and before its first Event is delivered. A Run that is
// refused at admission (draining, at capacity, past the activation limit)
// returns its error without the call, so an endpoint can still answer the
// request with a status of its own before it starts to stream. A nil sink
// still gets the call, and its Events are not delivered.
func (r *Runner) StreamRunAdmitted(ctx context.Context, request RunRequest, admitted func(), sink func(gotato.Event) error) (RunResponse, error) {
	if sink == nil {
		if admitted == nil {
			return r.run(ctx, request, nil)
		}
		// The call still fires on admission: the Events go nowhere.
		sink = func(gotato.Event) error { return nil }
	}
	if ctx == nil {
		ctx = context.Background()
	}
	type outcome struct {
		result RunResponse
		err    error
	}
	subs := make(chan gotato.Subscription, 1)
	admit := make(chan struct{}, 1)
	done := make(chan outcome, 1)
	go func() {
		result, err := r.run(ctx, request, &runStream{ready: func(sub gotato.Subscription) { subs <- sub }, admitted: admit})
		done <- outcome{result, err}
	}()
	var finished *outcome
	call := func() {
		if admitted != nil {
			admitted()
		}
	}
	select {
	case <-admit:
		call()
	case o := <-done:
		finished = &o
		select {
		case <-admit:
			call()
		default:
		}
	}
	if finished != nil {
		// The Run ended: it may have handed over its stream first.
		select {
		case sub := <-subs:
			deliver(ctx, sub, sink)
		default:
		}
		return finished.result, finished.err
	}
	select {
	case sub := <-subs:
		deliver(ctx, sub, sink)
	case o := <-done:
		finished = &o
		// A Run that ended may have handed over its stream first.
		select {
		case sub := <-subs:
			deliver(ctx, sub, sink)
		default:
		}
	}
	if finished == nil {
		o := <-done
		finished = &o
	}
	return finished.result, finished.err
}

func (r *Runner) run(ctx context.Context, request RunRequest, stream *runStream) (RunResponse, error) {
	return r.runWith(ctx, request, stream, false)
}

// runWith is run; wait makes the Run wait for a busy Session, as a Run that
// follows another does.
func (r *Runner) runWith(ctx context.Context, request RunRequest, stream *runStream, wait bool) (RunResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !request.Continue && strings.TrimSpace(request.Prompt) == "" {
		return RunResponse{}, notRunError(gotato.ErrorOf(gotato.CodeInvalidArgument, "service: prompt is required"))
	}
	if request.Continue && strings.TrimSpace(request.Prompt) != "" {
		return RunResponse{}, notRunError(gotato.ErrorOf(gotato.CodeInvalidArgument, "service: continue takes no prompt"))
	}
	// The Run registers as it is admitted, in one step, so the limits count it
	// at once and Drain and AbortSession reach it even while it is still
	// loading its Session.
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	handle := &runHandle{sessionID: request.SessionID, cancel: cancelRun, ready: make(chan struct{}), wait: wait, over: make(chan struct{})}
	if err := r.admit(handle); err != nil {
		why := "at_capacity"
		switch {
		case errors.Is(err, ErrDraining):
			why = "draining"
		case errors.Is(err, ErrOverloaded):
			why = "activation_limit"
		}
		r.notice(gotato.RunRejected{Header: gotato.NewHeader(gotato.SessionID(request.SessionID), "", 0), Why: why}, request.Agent)
		return RunResponse{}, err
	}
	if stream != nil && stream.admitted != nil {
		stream.admitted <- struct{}{}
	}
	r.notice(gotato.RunRequested{Header: gotato.NewHeader(gotato.SessionID(request.SessionID), "", 0), Agent: request.Agent}, request.Agent)
	release := sync.OnceFunc(r.release)
	followed := false
	defer func() {
		if !followed {
			r.unregisterHandle(handle)
			release()
		}
	}()
	out, err := r.serve(ctx, runCtx, request, stream, handle, nil)
	if next := handle.takeNext(); len(next) > 0 {
		// Messages the Run accepted after its last point of taking them run in
		// a Run that follows it, on this Instance and under the same
		// admission. This call returns with its own Run's result.
		followed = true
		r.followOn(ctx, handle, next, release)
		return out, err
	}
	if handle.stoppedByDrain() && out.Result.Status != gotato.RunStatusCompleted {
		// The drain stopped this Run, and it is saved and released. It
		// returns once every Run of the drain is, so the caller reconnects to
		// a Session that no Instance is still using.
		out.Reconnect = NewReconnect("draining")
		release()
		r.mu.Lock()
		drained := r.drained
		r.mu.Unlock()
		select {
		case <-drained:
		case <-ctx.Done():
		}
	}
	return out, err
}
