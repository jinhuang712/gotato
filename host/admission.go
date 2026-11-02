package host

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/host/internal/runs"
)

// Admitter decides whether a Run may start on this Instance. It is the one
// policy of admission the Runner does not own: a service has its own reasons
// (one tenant gets ten Runs, another fifty), and only the Runner knows what
// runs on this Instance, so the Admitter is given both, the Run and the
// Instance's Load. The caller's ctx carries who asks: a tenant or a user the
// service put there.
//
// What guards the Instance itself is not the Admitter's and runs before it: a
// draining Instance refuses (ErrDraining), and the memory bound
// (Bounds.MaxActiveSessions, with the eviction of warm copies) refuses a Run
// that would activate one Session too many (ErrOverloaded). Only a Run that
// passed both is asked.
//
// Admit returns a release func, which the Runner calls exactly once when the
// Run ends, whatever its outcome: a per-tenant count taken in Admit is given
// back there. release may be nil. A Run is refused with the error Admit
// returns: build it with gotato.ErrorOf (limit_exceeded, HTTP 429, is the code
// of a limit), and the Runner marks it not run, since nothing started, so a
// load balancer may send the Run to another Instance. An error that is not a
// gotato.RuntimeError is answered as limit_exceeded.
//
// Admit is called with the lock of the Runner's table of Runs held, so that
// Load and the Run it admits are one step and a limit never lets two Runs
// through for one place. It must return quickly, and it must not call the
// Runner. A Run that follows another with the messages the first left
// (FEATURES: leftovers) is part of the Run already admitted and is not asked
// again.
type Admitter interface {
	Admit(ctx context.Context, admission Admission, load Load) (release func(), err error)
}

// Admission names the Run an Admitter is asked about.
type Admission struct {
	// SessionID is the Session the Run is on: empty for a Run that creates one.
	SessionID string
	// Agent is the AgentProfile's name the request gives (RunRequest.Agent), or
	// the TaskSpec's AgentProfile for a Run of a TaskSpec: empty when the
	// request names none, and the Run takes the Session's AgentProfile, or the
	// default.
	Agent string
	// Task is the name of the TaskSpec the Run is of (RunRequest.Task, or the
	// Name of RunRequest.TaskSpec): empty for a Run that is of none.
	Task string
}

// Load is what this Instance holds when a Run is asked about, not counting the
// Run itself.
type Load struct {
	// ActiveRuns is the Runs in flight, following Runs included.
	ActiveRuns int
	// ActiveSessions is the Sessions those Runs are on, each counted once
	// however many Runs wait on it.
	ActiveSessions int
	// WarmSessions is the idle Sessions kept in memory (WarmPolicy).
	WarmSessions int
}

// Limits is the default Admitter: a bound on the Runs this Instance has in
// flight, whoever asks. The zero value admits every Run.
type Limits struct {
	// MaxActiveRuns is the limit on the Runs this Instance has in flight. Zero
	// means no limit; a negative value is refused by New.
	MaxActiveRuns int
}

// Admit refuses with ErrCapacity (limit_exceeded, not run and retryable) when
// the Instance has MaxActiveRuns Runs in flight already.
func (l Limits) Admit(_ context.Context, _ Admission, load Load) (func(), error) {
	if l.MaxActiveRuns > 0 && load.ActiveRuns >= l.MaxActiveRuns {
		return nil, ErrCapacity
	}
	return nil, nil
}

// Validate refuses a negative MaxActiveRuns, as a typo must not read as no
// limit. New calls it for an Admitter that has the method.
func (l Limits) Validate() error {
	if l.MaxActiveRuns < 0 {
		return errors.New("host: MaxActiveRuns cannot be negative")
	}
	return nil
}

// refusal is an Admitter's error on its way out of the table of Runs, so it
// is never taken for the Runner's own refusals.
type refusal struct{ err error }

func (r refusal) Error() string { return r.err.Error() }
func (r refusal) Unwrap() error { return r.err }

// admit decides whether a Run may start, and registers its handle when it
// may. In this order: a draining Instance refuses; a Run that would activate a
// Session past MaxActiveSessions is refused, because another Instance can
// still take it, unless a warm Session can be evicted to make room; then the
// Admitter decides. It returns the func that ends the admission, to call
// exactly once when the Run is over: the Admitter's release, then the count.
func (r *Runner) admit(ctx context.Context, handle *runHandle, request RunRequest, agent string) (func(), error) {
	admission := Admission{SessionID: handle.sessionID, Agent: agent}
	if request.task != nil {
		admission.Task = request.task.spec.Name
	}
	decide := func(counts runs.Counts) (release func(), err error) {
		defer func() {
			// [CONTRACT] a panic in the Admitter refuses the Run; it does not take the Runner down with the table's lock held.
			if p := recover(); p != nil {
				release, err = nil, refusal{fmt.Errorf("host: the admitter panicked: %v", p)}
			}
		}()
		release, err = r.admitter.Admit(ctx, admission, Load{ActiveRuns: counts.ActiveRuns, ActiveSessions: counts.ActiveSessions, WarmSessions: counts.WarmSessions})
		if err != nil {
			return nil, refusal{err}
		}
		return release, nil
	}
	memory := runs.Memory{MaxSessions: r.bounds.MaxActiveSessions, Cache: r.warm}
	for {
		victim, release, err := r.runs.Admit(handle, handle.sessionID, memory, decide)
		if victim != nil {
			// Evicting waits for an idle compaction to unwind, so it happens
			// without the table's lock; the count is taken again after it.
			r.dropEntry(victim, "evicted")
			continue
		}
		var refused refusal
		switch {
		case errors.Is(err, runs.ErrDraining):
			return nil, ErrDraining
		case errors.Is(err, runs.ErrOverloaded):
			return nil, ErrOverloaded
		case errors.As(err, &refused):
			return nil, refusedByAdmitter(refused.err)
		}
		return sync.OnceFunc(func() {
			// The Admitter's count is given back before the Run stops counting
			// as in flight, so a drain that saw the last Run end saw it all.
			if release != nil {
				release()
			}
			r.runs.Done()
		}), nil
	}
}

// refusedByAdmitter makes the Admitter's error the Run's: a Run it refuses
// never started, so it is marked not run, and an error that is not typed is
// answered as limit_exceeded. The Admitter's own error stays in the chain, for
// errors.Is.
func refusedByAdmitter(err error) error {
	var typed *gotato.RuntimeError
	if errors.As(err, &typed) {
		if typed.NotRun {
			return err
		}
		marked := *typed
		marked.NotRun, marked.Cause = true, err
		return &marked
	}
	refused := gotato.ErrorOf(gotato.CodeLimitExceeded, "host: "+err.Error())
	refused.NotRun, refused.Cause = true, err
	return refused
}
