package model

import (
	"fmt"
	"time"
)

// Retry is the third layer of a Client: how often to send a request again,
// for a request that was refused or never sent, and for a stream cut off.
// What is worth sending again is the Classifier's to say (Verdict.Retryable);
// Retry says how often and how long to wait.
type Retry struct {
	// Max is how many times a request is sent again after the first attempt.
	// Zero turns retries off, and so does the zero Retry.
	Max int
	// Backoff is the first wait, which grows linearly with the attempt and
	// carries up to 25% of jitter, so a fleet recovering from one outage does
	// not retry in lockstep. A provider's Retry-After or retry-after-ms wins
	// over it. With Max above 0, zero means DefaultRetry.Backoff.
	Backoff time.Duration
	// MaxWait caps every wait, a provider's Retry-After included: a provider
	// that asks for an hour does not hold a Run for one. With Max above 0, zero
	// means DefaultRetry.MaxWait.
	MaxWait time.Duration
}

// DefaultRetry is the Retry a Client uses unless WithRetry says otherwise:
// 2 retries, from 200 milliseconds, never waiting more than 60 seconds. Treat
// it as a constant.
var DefaultRetry = Retry{Max: 2, Backoff: 200 * time.Millisecond, MaxWait: 60 * time.Second}

// WithRetry sets the Client's Retry. WithRetry(Retry{}) turns retries off.
func WithRetry(retry Retry) Option {
	return func(o *options) { o.retry = retry }
}

// check refuses a Retry that cannot work and fills the zero fields of one that
// retries.
func (r Retry) checked() (Retry, error) {
	if r.Max < 0 || r.Backoff < 0 || r.MaxWait < 0 {
		return Retry{}, fmt.Errorf("model: Retry.Max, Backoff and MaxWait cannot be negative")
	}
	if r.Max == 0 {
		return Retry{}, nil
	}
	if r.Backoff == 0 {
		r.Backoff = DefaultRetry.Backoff
	}
	if r.MaxWait == 0 {
		r.MaxWait = DefaultRetry.MaxWait
	}
	return r, nil
}
