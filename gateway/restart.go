package gateway

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
// request-level retry in Client.do cannot see that, so the streams
// handle it here:
//
//   - before any event has been handed to the caller, the stream re-issues
//     the same request (within MaxRetries) and continues transparently: the
//     caller cannot observe a difference, so nothing is duplicated;
//   - after an event was delivered, the stream re-issues the same request
//     too, within the same MaxRetries, when a Session is listening: it tells
//     the Session with gotato.ReportModelRetry, which drops what it received
//     of the reply (a stream reader does on model_retried), and the new reply
//     is the whole reply. With no Session to drop the output, or when the
//     restarts run out, the stream fails with a Retryable *Error the caller
//     may act on.
//
// Framing and protocol errors (an oversized line, invalid JSON) are not
// interruptions and are never retried.

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
	body     []byte
	open     func(context.Context, []byte) (*http.Response, error)
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
	if r.restarts >= r.client.maxRetries {
		return nil, &Error{retryable: true, Message: streamInterruptedMessage, cause: readFailure{cause}}
	}
	delay := withJitter(r.client.retryBackoff * time.Duration(r.restarts+1))
	reported := gotato.ReportModelRetry(ctx, gotato.ModelRetry{Attempt: r.restarts + 1, Wait: delay, Error: "the stream was cut off"})
	if r.delivered && !reported {
		// Nobody drops the output already delivered, so a restart would
		// repeat it.
		return nil, &Error{retryable: true, Message: streamInterruptedMessage, cause: readFailure{cause}}
	}
	r.restarts++
	if err := wait(ctx, delay); err != nil {
		return nil, err
	}
	return r.open(ctx, r.body)
}

// readFailure carries the text of a read error as the cause of a cut stream
// without its chain: an Error that unwrapped to io.EOF would read as a clean
// end to a caller that tests errors.Is(err, io.EOF).
type readFailure struct{ error }

// Unwrap is nil on purpose, see readFailure.
func (readFailure) Unwrap() error { return nil }
