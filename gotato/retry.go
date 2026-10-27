package gotato

import (
	"context"
	"time"
)

// ModelRetry describes a call to the model that is about to be repeated.
type ModelRetry struct {
	// Attempt is the number of the attempt that failed, from 1.
	Attempt int
	// Wait is how long the gateway waits before it calls the model again.
	Wait time.Duration
	// Error says why the attempt failed, without anything a client should
	// not see.
	Error string
}

type retryReporterKey struct{}

func withRetryReporter(ctx context.Context, report func(ModelRetry)) context.Context {
	return context.WithValue(ctx, retryReporterKey{}, report)
}

// ReportModelRetry tells the Session that is calling the Model with ctx that
// the call is being repeated, so it stores a ModelRetried Event. A gateway
// that retries calls it before it waits. It does nothing, and returns false,
// when ctx did not come from a Session.
//
// A Session takes the report to mean that the reply so far is void: the same
// request is sent again, and the new reply is the whole reply. So a Model that
// retries after it has sent output sends the request again only when this
// returns true, and a caller that reads the stream without a Session never
// gets a reply twice.
func ReportModelRetry(ctx context.Context, retry ModelRetry) bool {
	if report, ok := ctx.Value(retryReporterKey{}).(func(ModelRetry)); ok {
		report(retry)
		return true
	}
	return false
}
