package host

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/profile"
)

// RunRequest is one unit of work.
type RunRequest struct {
	// SessionID names the Session. Empty creates a new Session.
	SessionID string
	// Agent selects the AgentProfile for a new Session; an existing Session
	// keeps the one it was created with unless Agent is set (a Session opened
	// for a TaskSpec keeps its own: it cannot be changed). Empty means the
	// Session's, else Config.DefaultAgent.
	Agent string
	// Task names a registered TaskSpec (profile.Registry). The Run opens a NEW
	// Session of the TaskSpec's AgentProfile, narrowed as the TaskSpec says,
	// with the TaskSpec's Background after the instruction, and the Record
	// keeps the TaskSpec as it was, so every Instance rebuilds the Session the
	// same after the registered one changes. It takes no SessionID and no Agent,
	// and excludes TaskSpec.
	Task string
	// TaskSpec is a TaskSpec the request carries, run as Task is. It excludes
	// Task. It must only narrow its AgentProfile (profile.TaskSpec.Narrow).
	TaskSpec *profile.TaskSpec
	// task is the TaskSpec the Run found, from Task or TaskSpec.
	task *taskRun
	// Prompt is the user input. It is required unless Continue is set.
	Prompt string
	// Continue resumes the loop of a Session without a new Message. It takes
	// no Prompt: a request with both is refused as invalid_argument, since the
	// Prompt would otherwise be dropped without a word.
	Continue bool
	// Metadata is applied to a new Session. A key under ReservedPrefix fails
	// the request with ErrReservedKey before anything is written.
	Metadata map[string]string
	// Update sets metadata on the Session before the Run, under its Claim,
	// as the Run's first write. An empty value deletes a key. A key under
	// ReservedPrefix fails the request with ErrReservedKey before anything is
	// written: the AgentProfile changes by Agent, not by Update.
	Update map[string]string
	// SkipSave runs without persisting the Session: a new Session lives only
	// for this Run, and a changed existing Session, Update included, is left
	// as it was.
	SkipSave bool
	// Timeout bounds this Run; the Run settles as deadline_exceeded. Zero
	// keeps the AgentProfile's RunTimeout; a negative value is refused as
	// invalid_argument (only a profile's RunTimeout may say "no deadline"). It is the caller's bound for this request
	// only: a Run that follows with the messages this one left keeps the
	// profile's RunTimeout.
	Timeout time.Duration
	// RequestID is the caller's own identifier of this request, such as one
	// it keeps for its retries. It is recorded in the Record, in the Run's
	// run_started Event (and so in its RunInfo), and echoed in
	// RunResponse.RequestID. Gotato does not deduplicate: a request sent again
	// with the same RequestID runs again, and the caller finds the earlier one
	// by reading the Record. It is for observing a request, not yet for
	// retrying it safely.
	RequestID string
}

// RunResponse is the outcome of one Run plus the Session state it left.
type RunResponse struct {
	// RequestID echoes RunRequest.RequestID, set when the Run was admitted.
	RequestID string           `json:"request_id,omitempty"`
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

// Run executes one Run synchronously. It returns a response and an error
// together, and the two answer different questions:
//
//   - err says whether the request succeeded as a whole: nil, a refusal before
//     the Run started (the response is the zero RunResponse and the error says
//     NotRun), or a failure of the Run itself or of saving it.
//   - A Run that started is settled in the response whatever err is: its
//     Result.Status, Result.Error, final text and usage describe it. A caller
//     that returns early on err != nil discards a settled Run, tools run and
//     all; read the response first when response.SessionID is not empty.
//   - err is non-nil for a Run that failed or was canceled, with the same error
//     as Result.Error, and for a Run whose Record could not be written
//     (ErrNotPersisted, with Result.Status failed). A Run that completed has
//     err nil, also when only the release of its Session failed.
//
// The zero response, with an empty SessionID, therefore means the request did
// not run; a non-empty SessionID means a Run started and settled.
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
// the Run is starting: admitted, holding its Session (opened or created), its
// Agent known, and its live Session built, and before its first Event is
// delivered. A Run that is refused before that (draining, at capacity, past
// the activation limit, busy here or on another Instance, a Session that does
// not exist, an unknown Agent, a failed open) returns its error without the
// call, so an endpoint can still answer the request with a status of its own
// before it starts to stream. A nil sink still gets the call, and its Events
// are not delivered.
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
	called := false
	call := func() {
		called = true
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
		// The Run ended: it may have handed over its stream first. A Run that
		// was refused before it started (no call, no result) delivers none of
		// it: the caller has answered with a status of its own.
		select {
		case sub := <-subs:
			if called || finished.result.SessionID != "" || finished.err == nil {
				deliver(ctx, sub, sink)
			} else {
				_ = sub.Close()
			}
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
		return RunResponse{}, notRunError(gotato.ErrorOf(gotato.CodeInvalidArgument, "host: prompt is required"))
	}
	if request.Continue && strings.TrimSpace(request.Prompt) != "" {
		return RunResponse{}, notRunError(gotato.ErrorOf(gotato.CodeInvalidArgument, "host: continue takes no prompt"))
	}
	// [CONTRACT] a key under the Runner's prefix is refused, not dropped or overridden, before the Run is admitted or anything is written.
	if err := refuseReserved(request.Metadata); err != nil {
		return RunResponse{}, err
	}
	if err := refuseReserved(request.Update); err != nil {
		return RunResponse{}, err
	}
	// [CONTRACT] a client's negative timeout is refused, not read as no bound.
	if request.Timeout < 0 {
		return RunResponse{}, notRunError(gotato.ErrorOf(gotato.CodeInvalidArgument, "host: timeout must not be negative"))
	}
	// The Run registers as it is admitted, in one step, so the limits count it
	// at once and Drain and AbortSession reach it even while it is still
	// loading its Session.
	task, err := r.resolveTask(ctx, request)
	if err != nil {
		return RunResponse{}, err
	}
	request.task = task
	// The AgentProfile the Run is for: the request's, or the TaskSpec's own.
	agent := request.Agent
	if task != nil {
		agent = task.spec.Profile
	}
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	handle := &runHandle{sessionID: request.SessionID, cancel: cancelRun, ready: make(chan struct{}), wait: wait, over: make(chan struct{})}
	release, err := r.admit(ctx, handle, request, agent)
	if err != nil {
		why := "at_capacity"
		switch {
		case errors.Is(err, ErrDraining):
			why = "draining"
		case errors.Is(err, ErrOverloaded):
			why = "activation_limit"
		}
		r.notice(RunRejected{Header: gotato.NewHeader(gotato.SessionID(request.SessionID), "", 0), Why: why}, agent)
		return RunResponse{}, err
	}
	r.notice(RunRequested{Header: gotato.NewHeader(gotato.SessionID(request.SessionID), "", 0), Agent: agent}, agent)
	followed := false
	defer func() {
		if !followed {
			r.unregisterHandle(handle)
			release()
		}
	}()
	out, err := r.serve(ctx, runCtx, request, stream, handle, nil)
	if out.SessionID != "" {
		out.RequestID = request.RequestID
	}
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
		select {
		case <-r.runs.Drained():
		case <-ctx.Done():
		}
	}
	return out, err
}
