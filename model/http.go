package model

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// send posts body with header to the endpoint and returns the first 2xx
// response. It is the one retry loop of every wire API: a transport error, or
// a failure the Classifier calls retryable, is repeated within Retry.Max, each
// time reported with gotato.ReportModelRetry before the wait. What differs
// between the wires is only the body and the headers, which they encoded.
func (c *Client) send(ctx context.Context, body []byte, header http.Header) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		response, retry, err := c.sendAttempt(ctx, body, header, attempt)
		if retry.Attempt == 0 {
			return response, err
		}
		gotato.ReportModelRetry(ctx, retry)
		if err := wait(ctx, retry.Wait); err != nil {
			return nil, err
		}
	}
}

// sendAttempt makes one attempt of send. It returns the response, the retry
// to report and wait for (Attempt is 0 when there is none), or the error that
// ends send.
func (c *Client) sendAttempt(ctx context.Context, body []byte, header http.Header, attempt int) (*http.Response, gotato.ModelRetry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.conn.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, gotato.ModelRetry{}, err
	}
	req.Header = header.Clone()
	response, err := c.httpClient.Do(req)
	if err != nil {
		retry, err := c.transportFailure(ctx, err, attempt)
		return nil, retry, err
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return c.successResponse(response, attempt)
	}
	retry, err := c.statusFailure(response, attempt)
	return nil, retry, err
}

// transportFailure returns the retry or the error for a request that could not be sent.
func (c *Client) transportFailure(ctx context.Context, err error, attempt int) (gotato.ModelRetry, error) {
	if ctx.Err() != nil {
		return gotato.ModelRetry{}, ctx.Err()
	}
	// The error text names the endpoint's URL and host; it stays the cause.
	failure := Failure{Message: transportMessage, Err: err}
	// The error text can name hosts, and Events reach clients.
	return c.retryOrFail(failure, nil, attempt, "the request could not be sent")
}

// successResponse returns a 2xx response that streams events, or the retry or
// the error for one whose body is a JSON object.
func (c *Client) successResponse(response *http.Response, attempt int) (*http.Response, gotato.ModelRetry, error) {
	if !startsWithJSONObject(response) {
		return response, gotato.ModelRetry{}, nil
	}
	// A 2xx whose body is a JSON object, not a stream of events, is a
	// provider's error told with a success status.
	refusal := readErrorBody(response.Body)
	_ = response.Body.Close()
	failure := Failure{Message: refusal.Message, Code: refusal.Code, Type: refusal.Type, Header: response.Header}
	retry, err := c.retryOrFail(failure, response.Header, attempt, "the provider answered with an error")
	return nil, retry, err
}

// statusFailure returns the retry or the error for a response that is not 2xx.
func (c *Client) statusFailure(response *http.Response, attempt int) (gotato.ModelRetry, error) {
	refusal := readErrorBody(response.Body)
	_ = response.Body.Close()
	failure := Failure{Status: response.StatusCode, Message: refusal.Message, Code: refusal.Code, Type: refusal.Type, Header: response.Header}
	return c.retryOrFail(failure, response.Header, attempt, fmt.Sprintf("the provider answered %d", response.StatusCode))
}

// retryOrFail returns the retry to report, with text as its error, when the
// Classifier calls failure retryable and attempts remain; otherwise the error.
func (c *Client) retryOrFail(failure Failure, header http.Header, attempt int, text string) (gotato.ModelRetry, error) {
	verdict := c.classify(failure)
	if verdict.Retryable && attempt < c.retry.Max {
		return gotato.ModelRetry{Attempt: attempt + 1, Wait: c.retryWait(header, attempt), Error: text}, nil
	}
	return gotato.ModelRetry{}, newError(failure, verdict)
}

// jitterDivisor sets the most jitter a backoff takes: a quarter of it.
const jitterDivisor = 4

// withJitter adds up to 25% to a backoff so a fleet recovering from the same
// outage does not retry in lockstep. The delay never shrinks below the base.
func withJitter(delay time.Duration) time.Duration {
	if delay <= 0 {
		return delay
	}
	span := delay / jitterDivisor
	if span <= 0 {
		return delay
	}
	return delay + time.Duration(rand.Int63n(int64(span)+1))
}

func retryAfterDuration(header http.Header) time.Duration {
	if value := strings.TrimSpace(header.Get("retry-after-ms")); value != "" {
		if millis, err := time.ParseDuration(value + "ms"); err == nil && millis >= 0 {
			return millis
		}
	}
	value := strings.TrimSpace(header.Get("retry-after"))
	if value == "" {
		return -1
	}
	if seconds, err := time.ParseDuration(value + "s"); err == nil && seconds >= 0 {
		return seconds
	}
	if when, err := http.ParseTime(value); err == nil {
		if delay := time.Until(when); delay > 0 {
			return delay
		}
		return 0
	}
	return -1
}

func wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func cloneHeaders(headers map[string]string) map[string]string {
	out := make(map[string]string, len(headers))
	maps.Copy(out, headers)
	return out
}

// startsWithJSONObject reports whether the response body begins with '{' after
// white space, which no event stream does: a stream begins with data:, event:,
// id:, retry: or a comment. The bytes looked at stay in the body.
func startsWithJSONObject(response *http.Response) bool {
	reader := bufio.NewReader(response.Body)
	response.Body = struct {
		io.Reader
		io.Closer
	}{reader, response.Body}
	// [LIVENESS] one byte at a time: a Peek of many bytes would block until they arrived and stall the first event of a stream.
	for i := range 256 {
		peeked, err := reader.Peek(i + 1)
		if err != nil {
			return false
		}
		switch peeked[i] {
		case ' ', '\t', '\r', '\n':
		case '{':
			return true
		default:
			return false
		}
	}
	return false
}
