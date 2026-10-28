package gateway

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// do sends body to the endpoint and returns the first 2xx response. It is the
// one retry loop of every wire API: a transport error, or a status that
// retryableStatus accepts, is repeated within MaxRetries, each time reported
// with gotato.ReportModelRetry before the wait. What differs between the APIs
// is only authentication and protocol headers, which setProtocolHeaders sets.
func (c *Client) do(ctx context.Context, body []byte) (*http.Response, error) {
	// The Responses API has no anonymous mode, so a missing key is refused
	// before any request; the other two APIs send no credential without one,
	// which keyless local gateways rely on.
	if c.api == APIResponses && strings.TrimSpace(c.apiKey) == "" {
		return nil, &Error{Message: "api_key is required for the Responses API"}
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		// Configurable and per-request headers are applied first so protocol
		// headers from this adapter win; neither may disable authentication
		// or break SSE negotiation.
		c.applyHeaders(ctx, req)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		c.setProtocolHeaders(req)
		response, err := c.httpClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if !transientTransport(err) {
				return nil, &Error{Message: err.Error(), cause: err}
			}
			if attempt < c.maxRetries {
				delay := withJitter(c.retryBackoff * time.Duration(attempt+1))
				// The error text can name hosts, and Events reach clients.
				gotato.ReportModelRetry(ctx, gotato.ModelRetry{Attempt: attempt + 1, Wait: delay, Error: "the request could not be sent"})
				if err := wait(ctx, delay); err != nil {
					return nil, err
				}
				continue
			}
			return nil, &Error{retryable: true, Message: err.Error(), cause: err}
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return response, nil
		}
		body := readErrorBody(response.Body)
		message := body.Message
		_ = response.Body.Close()
		retryable := retryableStatus(response.StatusCode, message)
		if retryable && attempt < c.maxRetries {
			delay := c.retryDelay(response.Header, attempt)
			gotato.ReportModelRetry(ctx, gotato.ModelRetry{Attempt: attempt + 1, Wait: delay, Error: fmt.Sprintf("the provider answered %d", response.StatusCode)})
			if err := wait(ctx, delay); err != nil {
				return nil, err
			}
			continue
		}
		return nil, c.overflow.httpError(response.StatusCode, retryable, body)
	}
}

// retryDelay returns how long to wait before the next attempt. A provider
// Retry-After/retry-after-ms header wins; otherwise the configured backoff
// grows linearly with the attempt and carries jitter.
func (c *Client) retryDelay(header http.Header, attempt int) time.Duration {
	if retryAfter := retryAfterDuration(header); retryAfter >= 0 {
		return retryAfter
	}
	return withJitter(c.retryBackoff * time.Duration(attempt+1))
}

// withJitter adds up to 25% to a backoff so a fleet recovering from the same
// outage does not retry in lockstep. The delay never shrinks below the base.
func withJitter(delay time.Duration) time.Duration {
	if delay <= 0 {
		return delay
	}
	span := delay / 4
	if span <= 0 {
		return delay
	}
	return delay + time.Duration(rand.Int63n(int64(span)+1))
}

// retryableStatus is the shared retry policy for every wire API. A 429
// that names a terminal quota is not retryable; transient 429/5xx responses
// and known overload phrasing are.
func retryableStatus(status int, message string) bool {
	if status == http.StatusTooManyRequests && terminalQuotaLimit(message) {
		return false
	}
	return status == http.StatusTooManyRequests || status == http.StatusInternalServerError ||
		status == http.StatusBadGateway || status == http.StatusServiceUnavailable ||
		status == http.StatusGatewayTimeout || status == statusOverloaded || strings.Contains(strings.ToLower(message), "rate limit") ||
		strings.Contains(strings.ToLower(message), "overloaded") || strings.Contains(strings.ToLower(message), "service unavailable")
}

// terminalQuotaLimit reports whether a 429 message names a quota that
// retrying cannot fix (an exhausted balance or usage cap) rather than a
// transient rate limit.
func terminalQuotaLimit(message string) bool {
	lower := strings.ToLower(message)
	for _, phrase := range []string{"usage limit", "freeusagelimiterror", "gousagelimiterror", "insufficient_quota", "out of budget", "quota exceeded", "available balance"} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
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
