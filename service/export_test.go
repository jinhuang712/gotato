package service

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
