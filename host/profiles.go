package host

import (
	"context"
	"errors"
	"fmt"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/profile"
)

// taskRun is the TaskSpec a new Session is opened for: the spec a Run found
// (registered by name, or carried by the request) and the document the Record
// keeps of it. The Record's copy is what every Instance builds the Session from
// after the registered spec changes.
type taskRun struct {
	spec     profile.TaskSpec
	snapshot string
}

// resolveTask finds the TaskSpec a Session is opened for: the registered one
// by name, or the value as given, and nil for neither. A Run with no Session and
// CreateSession both ask, so the two write the same Record. Naming both, or an
// Agent besides, is refused: a TaskSpec opens a Session of its own
// AgentProfile. The spec is checked against that AgentProfile when the Session
// is built (newSession), before anything is written.
func (r *Runner) resolveTask(ctx context.Context, name string, value *profile.TaskSpec, agent string) (*taskRun, error) {
	if name == "" && value == nil {
		return nil, nil
	}
	switch {
	case name != "" && value != nil:
		return nil, notRunError(gotato.ErrorOf(gotato.CodeInvalidArgument, "host: Task and TaskSpec are exclusive: a request names a registered TaskSpec or carries one"))
	case agent != "":
		return nil, notRunError(gotato.ErrorOf(gotato.CodeInvalidArgument, "host: a Session of a TaskSpec runs the TaskSpec's own AgentProfile, so it takes no Agent"))
	}
	var spec profile.TaskSpec
	if value != nil {
		spec = *value
	} else {
		var err error
		if spec, err = r.profiles.TaskSpec(ctx, name); err != nil {
			if errors.Is(err, profile.ErrNotRegistered) {
				return nil, ErrUnknownTask
			}
			return nil, profileError(err)
		}
	}
	snapshot, err := profile.EncodeTaskSpec(spec)
	if err != nil {
		return nil, profileError(err)
	}
	return &taskRun{spec: spec, snapshot: string(snapshot)}, nil
}

// resolveNarrowed is the AgentProfile of a TaskSpec as it runs it, checked.
func (r *Runner) resolveNarrowed(ctx context.Context, task *taskRun) (profile.AgentProfile, error) {
	p, err := r.profiles.Narrowed(ctx, task.spec)
	return p, profileError(err)
}

// agentNamed resolves the AgentProfile a request names, the default for an
// empty name. It looks the name up now: a profile registered or replaced since
// reaches this call.
func (r *Runner) agentNamed(ctx context.Context, name string) (profile.AgentProfile, error) {
	if name == "" {
		name = r.defaultAgent
	}
	p, err := r.profiles.AgentProfile(ctx, name)
	return p, profileError(err)
}

// getter reads one metadata value, as a Record does.
type getter interface {
	Get(key string) (string, bool)
}

// agentOf resolves the AgentProfile a Session runs now: the one it names, and
// as its TaskSpec narrows it when it was opened for one, the TaskSpec the Record
// kept and not the registered one. Every build of a live Session asks, so a
// replaced profile reaches the Sessions built after it, and a Run in flight
// keeps the value it was built from.
func (r *Runner) agentOf(ctx context.Context, s getter) (profile.AgentProfile, error) {
	if doc, ok := s.Get(metaTask); ok {
		spec, err := profile.DecodeTaskSpec("", []byte(doc))
		if err != nil {
			return profile.AgentProfile{}, profileError(err)
		}
		p, err := r.profiles.Narrowed(ctx, spec)
		return p, profileError(err)
	}
	name, _ := s.Get(metaAgent)
	return r.agentNamed(ctx, name)
}

// profileError makes the error of a lookup the error of a Run that is refused
// before it starts: a name nothing is registered under is ErrUnknownAgent, an
// invalid definition is its own invalid_argument error, and a store that failed
// is a store failure. The cause stays in the chain, for errors.Is.
func profileError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, profile.ErrNotRegistered) {
		return ErrUnknownAgent
	}
	var typed *gotato.RuntimeError
	if errors.As(err, &typed) {
		marked := *typed
		marked.NotRun, marked.Cause = true, err
		return &marked
	}
	refused := gotato.ErrorOf(gotato.CodeStoreFailure, fmt.Sprintf("host: %v", err))
	refused.NotRun, refused.Cause = true, err
	return refused
}
