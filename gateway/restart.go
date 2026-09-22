package gateway

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"syscall"
	"time"
)

// A streaming response can fail after its headers arrived: a provider or a
// proxy resets the connection, or closes it before the terminal event. The
// request-level retry in do/doResponses cannot see that, so the streams
// handle it here:
//
//   - before any event has been handed to the caller, the stream re-issues
//     the same request (within MaxRetries) and continues transparently: the
//     caller cannot observe a difference, so nothing is duplicated;
//   - after an event was delivered, replaying would duplicate output, so the
//     stream fails with a Retryable *Error the caller may act on.
//
// Framing and protocol errors (an oversized line, invalid JSON) are not
// interruptions and are never retried.

// interrupted reports whether a read error is a transport interruption.
func interrupted(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) ||
		errors.As(err, &netErr)
}

// restarter re-issues one streaming request after an interruption.
type restarter struct {
	client   *Client
	body     []byte
	open     func(context.Context, []byte) (*http.Response, error)
	restarts int
	// delivered is set once an event reached the caller.
	delivered bool
}

// recover decides what an interrupted read becomes. It returns a new
// response when the stream may restart, or the error to surface.
func (r *restarter) recover(ctx context.Context, cause error) (*http.Response, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if !interrupted(cause) {
		return nil, cause
	}
	if r.delivered || r.restarts >= r.client.maxRetries {
		return nil, &Error{Retryable: true, Message: "stream interrupted: " + cause.Error()}
	}
	r.restarts++
	if err := wait(ctx, withJitter(r.client.retryBackoff*time.Duration(r.restarts))); err != nil {
		return nil, err
	}
	return r.open(ctx, r.body)
}
