package model

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"syscall"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// A streaming response can fail after its headers arrived: a provider or a
// proxy resets the connection, or closes it before the terminal event. The
// request-level retry in Client.send cannot see that, so the stream loop
// handles it here, for every wire alike, since nothing under it reads the
// bytes:
//
//   - before any event has been handed to the caller, the stream re-issues
//     the same request (within Retry.Max) and continues transparently: the
//     caller cannot observe a difference, so nothing is duplicated;
//   - after an event was delivered, the stream re-issues the same request
//     too, within the same Retry.Max, when a Session is listening: it tells
//     the Session with gotato.ReportModelRetry, which drops what it received
//     of the reply (a stream reader does on model_retried), and the new reply
//     is the whole reply. With no Session to drop the output, or when the
//     restarts run out, the stream fails with a Retryable *Error the caller
//     may act on.
//
// Framing and protocol errors (an oversized line, invalid JSON) are not
// interruptions and are never retried.

// [SECURITY] a fixed text, so the read error that may name a host never reaches the Model or a client.
// streamInterruptedMessage is the public Message of a stream cut off; the
// read error, which can name a remote address, stays the cause.
const streamInterruptedMessage = "stream interrupted"

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
	open     func(context.Context) (*http.Response, error)
	restarts int
	// delivered is set once an event reached the caller. It does not stop a
	// restart: the caller is told, and drops what it received.
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
	// [FAILURE-CLASS] the cause keeps the text but not the chain: an Error that unwrapped to io.EOF would read as a clean end.
	failure := Failure{Message: streamInterruptedMessage, Err: errors.New(cause.Error())}
	verdict := r.client.classify(failure)
	if !verdict.Retryable || r.restarts >= r.client.retry.Max {
		return nil, newError(failure, verdict)
	}
	delay := min(withJitter(r.client.retry.Backoff*time.Duration(r.restarts+1)), r.client.retry.MaxWait)
	reported := gotato.ReportModelRetry(ctx, gotato.ModelRetry{Attempt: r.restarts + 1, Wait: delay, Error: "the stream was cut off"})
	if r.delivered && !reported {
		// [DATA-LOSS] a restart after delivered output would repeat that output when nobody drops it.
		return nil, newError(failure, verdict)
	}
	r.restarts++
	if err := wait(ctx, delay); err != nil {
		return nil, err
	}
	return r.open(ctx)
}
