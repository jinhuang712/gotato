package service

import "github.com/jinhuang712/gotato/gotato"

// The calls below take a trailing list of options so that they can grow in
// later releases without a change of signature. Each call has its own option
// type; a type with no option function yet is kept for that reason.

// AbortOption configures AbortSession.
type AbortOption func(*abortConfig)

type abortConfig struct {
	runID gotato.RunID
}

// AbortRun names the Run that AbortSession is to stop. A call that names a Run
// stops only that Run: when the Run is over already, or is not the one going on
// the Session, nothing is stopped and the call returns ErrNoRun, which for a
// named Run means "already stopped, safe to ignore". A caller that repeats an
// abort whose answer it lost passes the Run's ID (RunResponse.Result.RunID, or
// the run_started Event), so the repeat can never stop the Run that follows.
func AbortRun(id gotato.RunID) AbortOption {
	return func(c *abortConfig) { c.runID = id }
}

// ListOption configures Sessions. It has no option yet.
type ListOption func(*listConfig)

type listConfig struct{}

// EventsOption configures Runner.Events. It has no option yet.
type EventsOption func(*eventsConfig)

type eventsConfig struct{}

// StreamRunOption configures StreamRun and StreamRunAdmitted. It has no option
// yet.
type StreamRunOption func(*streamRunConfig)

type streamRunConfig struct{}

// SessionStreamOption configures StreamSession. It has no option yet.
type SessionStreamOption func(*sessionStreamConfig)

type sessionStreamConfig struct{}
