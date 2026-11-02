package host

import "github.com/jinhuang712/gotato/gotato"

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
