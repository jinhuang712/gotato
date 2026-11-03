package host

import (
	"context"
	"strings"

	"github.com/jinhuang712/gotato/record"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/profile"
	"github.com/jinhuang712/gotato/store"
)

// CreateSessionRequest is the one way to make a Session. The Session starts
// with the history in Messages and the Agent it names (or the TaskSpec it is
// opened for), and nothing else of it is set by the caller: its instruction, its
// ceiling and its Tools are its Agent's, as a TaskSpec narrows them.
type CreateSessionRequest struct {
	// Agent names the AgentProfile the Session runs. Empty means the default
	// (Config.DefaultAgent). It excludes Task and TaskSpec.
	Agent string
	// Task names a registered TaskSpec (profile.Registry). The Session is of
	// the TaskSpec's AgentProfile, narrowed as the TaskSpec says, with the
	// TaskSpec's Background after the instruction, and the Record keeps the
	// TaskSpec as it was: exactly the Session RunRequest.Task opens. Run it by
	// its ID like any other; a caller-chosen ID makes this call the point to
	// retry by. It excludes TaskSpec and Agent.
	Task string
	// TaskSpec is a TaskSpec the request carries, opened as Task is. It excludes
	// Task and Agent. It must only narrow its AgentProfile
	// (profile.TaskSpec.Narrow).
	TaskSpec *profile.TaskSpec
	// ID is the new Session's ID. Empty generates one. An ID that is taken fails
	// with ErrSessionExists: the store's Create is the check, so two concurrent
	// creates of the same ID cannot both succeed, also on two Instances.
	ID string
	// Metadata is the caller's own. A key under ReservedPrefix fails the request
	// with ErrReservedKey, before anything is written.
	Metadata map[string]string
	// Messages is the history the Session starts with.
	Messages []gotato.Message
}

// CreateSession creates and saves a Session bound to an Agent, and returns its
// listing columns: a value, so what a caller holds is never mistaken for the
// stored Record, which only the Runner writes. Read the Record with Record.
func (r *Runner) CreateSession(ctx context.Context, request CreateSessionRequest) (record.Info, error) {
	if err := refuseReserved(request.Metadata); err != nil {
		return record.Info{}, err
	}
	task, err := r.resolveTask(ctx, request.Task, request.TaskSpec, request.Agent)
	if err != nil {
		return record.Info{}, err
	}
	var options []record.Option
	if request.ID != "" {
		if strings.TrimSpace(request.ID) == "" {
			return record.Info{}, gotato.ErrorOf(gotato.CodeInvalidArgument, "host: session ID is blank")
		}
		options = append(options, record.WithID(request.ID))
	}
	if len(request.Messages) > 0 {
		options = append(options, record.WithMessages(request.Messages...))
	}
	s, spec, err := r.newSession(ctx, request.Agent, task, request.Metadata, options...)
	if err != nil {
		return record.Info{}, err
	}
	// Nothing runs against a Session that is only created: it is released in
	// the write that creates it.
	if _, err := r.create(ctx, s, spec.Name, store.WithIdle()); err != nil {
		return record.Info{}, err
	}
	return s.Info(), nil
}

// newSession builds a Session bound to an AgentProfile, without saving it: the
// request's Agent, or the default; and, for a Run of a TaskSpec, the TaskSpec's
// own AgentProfile as the TaskSpec narrows it, with the TaskSpec kept in the
// Session's metadata. A profile that is not there fails before the Session
// exists.
func (r *Runner) newSession(ctx context.Context, agent string, task *taskRun, metadata map[string]string, options ...record.Option) (*record.Record, profile.AgentProfile, error) {
	var spec profile.AgentProfile
	var err error
	if task != nil {
		spec, err = r.resolveNarrowed(ctx, task)
	} else {
		spec, err = r.agentNamed(ctx, agent)
	}
	if err != nil {
		return nil, profile.AgentProfile{}, err
	}
	s := record.New(options...)
	for key, value := range metadata {
		s.Set(key, value)
	}
	s.Set(metaAgent, spec.Name)
	if task != nil {
		s.Set(metaTask, task.snapshot)
	}
	return s, spec, nil
}

// DeleteSession removes a Session under the Session lock, so a Run in flight
// is never resurrected by a delete or vice versa. A Run going on this Instance
// makes it busy, as for any other change; a Run going on another live Instance
// is not deleted from under it: the call returns not_writer (NotRun,
// Retryable) with the writer's advertised address, as Send does. A Session
// whose writer is gone is deleted.
//
// The store's Delete is not conditional, so the delete claims the Session
// first, as a Run does. An Instance that starts a Run after the look at the
// writer either wins the claim, and the delete is refused as busy, or loses it.
func (r *Runner) DeleteSession(ctx context.Context, sessionID string) error {
	return r.withSessionLock(ctx, sessionID, func() error {
		// The answer for a Run going on another live Instance, with its
		// address. It is a look only: the claim below is the check.
		if err := r.checkWriter(ctx, sessionID); err != nil {
			return err
		}
		warm := r.warm.Get(sessionID)
		// The store's Delete is not conditional, so the Instance that deletes
		// holds the Claim first. The claim is a conditional write: an Instance
		// that claims the Session after the look above either wins it, and
		// this call is refused as busy, or loses it to this one.
		writer, early, _, err := r.acquire(ctx, sessionID)
		if err != nil {
			return err
		}
		agent, _ := writer.Record().Get(metaAgent)
		r.publishOpened(writer, early, agent)
		if err := r.store.Records.Delete(ctx, sessionID); err != nil {
			// The Claim was written and the Record stays: give it back.
			_ = r.releaseWriter(ctx, writer)
			return storeFailureOf(ctx, err, true)
		}
		if warm != nil {
			r.unloaded(sessionID, warm.Agent, "deleted")
		}
		r.notice(SessionDeleted{Header: gotato.NewHeader(gotato.SessionID(sessionID), "", 0)}, "")
		r.endSessionStreams(sessionID, record.ErrNotFound)
		return nil
	})
}

// Fork creates a new Session from an existing one, and returns its listing
// columns. An explicit newID must be free: a fork never overwrites an existing
// Session, and a taken ID is ErrSessionExists, as CreateSession reports it.
func (r *Runner) Fork(ctx context.Context, sessionID, newID string) (record.Info, error) {
	parent, err := store.View(ctx, r.store.Records, sessionID)
	if err != nil {
		return record.Info{}, storeFailureOf(ctx, err, true)
	}
	var options []record.Option
	if newID != "" {
		options = append(options, record.WithID(newID))
	}
	child := record.Fork(parent, options...)
	// The child starts with its parent's history, so it keeps its parent's
	// cache key and its first Turn reads the parent's cached prefix.
	child.Set(metaCacheKey, sessionCacheKey(parent))
	// The store's Create is the existence check, so two concurrent forks with
	// the same explicit newID cannot both succeed.
	childAgent, _ := child.Get(metaAgent)
	if _, err := r.create(ctx, child, childAgent, store.WithIdle()); err != nil {
		return record.Info{}, err
	}
	return child.Info(), nil
}

// AbortSession stops the Run in flight on a Session: everything in flight is
// canceled through its context, and the Run settles as canceled with every
// Turn it committed. It is the Stop of the messages a Run takes (Send).
//
// A Run going on another live Instance is not stopped from here: the call
// returns not_writer (NotRun, Retryable) with the writer's advertised address,
// as Send does. With no Run going anywhere it returns ErrNoRun, and for a
// Session the store does not know (or refuses the caller), not_found.
//
// The call names a Session, so a repeat of an abort whose answer was lost
// stops whatever Run is going now, which may be the next one. A caller that
// retries passes AbortRun with the Run's ID: only that Run is stopped, and a
// Run that is over already is ErrNoRun, which then means "already stopped".
func (r *Runner) AbortSession(ctx context.Context, sessionID string, opts ...AbortOption) error {
	var config abortConfig
	for _, option := range opts {
		if option != nil {
			option(&config)
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	handles := r.runs.Of(sessionID)
	// The store answers first, whatever stage the Run is in (waiting, loading,
	// building or live), so that a caller the store refuses cannot stop a Run
	// at a stage where nothing was read yet.
	if len(handles) > 0 {
		if err := r.reachStore(ctx, sessionID); err != nil {
			return err
		}
	}
	if r.abortSession(sessionID, config.runID) {
		return nil
	}
	if err := r.checkWriter(ctx, sessionID); err != nil {
		return err
	}
	return ErrNoRun
}

// abortSession cancels the started Run for a Session, or the oldest Run still
// waiting for its lock when none has started. With runID it cancels that Run
// only, and nothing when it is not going here.
func (r *Runner) abortSession(sessionID string, runID gotato.RunID) bool {
	waiting := r.runs.Of(sessionID)
	var chosen *runHandle
	for _, handle := range waiting {
		handle.mu.Lock()
		// A settled Run is over: naming it is "already stopped", and it
		// is not the Run that follows.
		started, named := handle.runID != "" && !handle.settled, handle.runID == runID
		handle.mu.Unlock()
		if started && (runID == "" || named) {
			chosen = handle
			break
		}
	}
	if chosen == nil && runID == "" && len(waiting) > 0 {
		chosen = waiting[0]
	}
	if chosen == nil {
		return false
	}
	chosen.stop()
	return true
}
