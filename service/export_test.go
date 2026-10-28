package service

import "time"

// SetBeforeDetach sets the seam that holds a Run after it has settled and
// before its live Session stops taking messages.
func (r *Runner) SetBeforeDetach(fn func()) { r.beforeDetach = fn }

// SetBeforeRelease sets the seam that holds a Run after it has stopped taking
// messages and before it releases its Session.
func (r *Runner) SetBeforeRelease(fn func()) { r.beforeRelease = fn }

// Clock and Timer are the time a Runner waits by, which a test advances itself.
type (
	Clock = clock
	Timer = timer
)

// WithClock returns cfg with its warm Sessions timed by c, so no test waits
// for a TTL.
func WithClock(cfg Config, c Clock) Config {
	cfg.clock = c
	return cfg
}

// SetOnSendRetry sets the seam that counts the rounds of Send's loop.
func (r *Runner) SetOnSendRetry(fn func()) { r.onSendRetry = fn }

// SetDrainStopWait sets how long Drain waits for the Runs it stopped.
func (r *Runner) SetDrainStopWait(d time.Duration) { r.drainStop = d }

// WarmLimit returns the bound on warm Sessions in force.
func (r *Runner) WarmLimit() int { return r.admission.MaxWarmSessions }

// RemoveAgent unregisters an Agent, as a deploy that drops it would. Call it
// only while no Run reads the Agents.
func (r *Runner) RemoveAgent(name string) { delete(r.agents, name) }

// MarkUnreleased, RepairReleases and Unreleased expose the repair of release
// writes to a test, which drives a pass itself instead of waiting for a tick.
func (r *Runner) MarkUnreleased(id string) { r.markUnreleased(id) }

// RepairReleases runs one repair pass.
func (r *Runner) RepairReleases() { r.repairReleases() }

// Unreleased reports whether a Session is remembered as not released.
func (r *Runner) Unreleased(id string) bool {
	r.releaseMu.Lock()
	defer r.releaseMu.Unlock()
	_, ok := r.unreleased[id]
	return ok
}
