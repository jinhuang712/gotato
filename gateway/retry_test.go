package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// countingServer answers 429 with "Retry-After: 0" (so a retry waits for
// nothing) for the first fail calls, then a whole chat reply. It counts calls.
func countingServer(t *testing.T, fail int, status int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if int(calls.Add(1)) <= fail {
			w.Header().Set("Retry-After", "0")
			http.Error(w, `{"error":{"message":"try later"}}`, status)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(chatText("ok") + chatStop))
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func chatClientAt(t *testing.T, endpoint string, opts ...Option) *Client {
	t.Helper()
	client, err := New(ChatCompletions{}, Connection{Endpoint: endpoint}, modelOf(gotato.ModelFacts{}), opts...)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestRetryZeroValueTurnsRetriesOff(t *testing.T) {
	server, calls := countingServer(t, 1, http.StatusServiceUnavailable)
	client := chatClientAt(t, server.URL, WithRetry(Retry{}))
	_, err := client.Stream(context.Background(), gotato.ModelRequest{})
	var gerr *Error
	if !errors.As(err, &gerr) || gerr.StatusCode != 503 || !gerr.Retryable() || calls.Load() != 1 {
		t.Fatalf("err = %v, calls = %d", err, calls.Load())
	}
}

// With no WithRetry the Client sends again twice: three attempts in all. The
// provider's Retry-After (0 here) wins over the backoff, so this is fast.
func TestTheDefaultRetryIsTwoRetries(t *testing.T) {
	server, calls := countingServer(t, 2, http.StatusTooManyRequests)
	client := chatClientAt(t, server.URL)
	stream, err := client.Stream(context.Background(), gotato.ModelRequest{})
	if err != nil || calls.Load() != 3 {
		t.Fatalf("err = %v, calls = %d, want 3", err, calls.Load())
	}
	_ = stream.Close()

	server, calls = countingServer(t, 3, http.StatusTooManyRequests)
	client = chatClientAt(t, server.URL)
	_, err = client.Stream(context.Background(), gotato.ModelRequest{})
	var gerr *Error
	if !errors.As(err, &gerr) || gerr.StatusCode != 429 || calls.Load() != 3 {
		t.Fatalf("err = %v, calls = %d, want a 429 after 3 attempts", err, calls.Load())
	}
}

func TestRetryMaxIsHonoured(t *testing.T) {
	for _, max := range []int{1, 3, 5} {
		server, calls := countingServer(t, 100, http.StatusServiceUnavailable)
		client := chatClientAt(t, server.URL, WithRetry(Retry{Max: max, Backoff: time.Millisecond, MaxWait: time.Second}))
		_, err := client.Stream(context.Background(), gotato.ModelRequest{})
		if err == nil || int(calls.Load()) != max+1 {
			t.Errorf("Max %d: err = %v, calls = %d, want %d", max, err, calls.Load(), max+1)
		}
	}
}

// A retry that fixes only some of its fields takes the default of the rest.
func TestRetryFieldsLeftZeroTakeTheDefaultsWhenRetrying(t *testing.T) {
	for _, tc := range []struct{ in, want Retry }{
		{Retry{Max: 1}, Retry{Max: 1, Backoff: DefaultRetry.Backoff, MaxWait: DefaultRetry.MaxWait}},
		{Retry{Max: 4, Backoff: time.Second}, Retry{Max: 4, Backoff: time.Second, MaxWait: DefaultRetry.MaxWait}},
		{Retry{Max: 4, MaxWait: time.Second}, Retry{Max: 4, Backoff: DefaultRetry.Backoff, MaxWait: time.Second}},
		{Retry{Backoff: time.Second, MaxWait: time.Minute}, Retry{}},
		{Retry{}, Retry{}},
	} {
		client := chatClientAt(t, "http://127.0.0.1:1/chat", WithRetry(tc.in))
		if client.retry != tc.want {
			t.Errorf("WithRetry(%+v) = %+v, want %+v", tc.in, client.retry, tc.want)
		}
	}
}

// The provider's Retry-After never holds a Run longer than MaxWait.
func TestRetryAfterIsCappedByMaxWait(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "3600")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(chatText("ok") + chatStop))
	}))
	defer server.Close()
	client := chatClientAt(t, server.URL, WithRetry(Retry{Max: 1, Backoff: time.Millisecond, MaxWait: 30 * time.Millisecond}))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	stream, err := client.Stream(ctx, gotato.ModelRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Retry-After: 3600 was waited out: %v", elapsed)
	}
	if calls.Load() != 2 {
		t.Fatalf("attempts = %d, want 2", calls.Load())
	}
}

// retryWait is the backoff, which grows with the attempt, or a Retry-After,
// and never more than MaxWait.
func TestRetryWaitGrowsFromTheBackoffAndIsCapped(t *testing.T) {
	client := chatClientAt(t, "http://127.0.0.1:1/chat", WithRetry(Retry{Max: 5, Backoff: 100 * time.Millisecond, MaxWait: 250 * time.Millisecond}))
	// Jitter adds up to a quarter.
	for attempt, base := range []time.Duration{100 * time.Millisecond, 200 * time.Millisecond} {
		got := client.retryWait(http.Header{}, attempt)
		if got < base || got > base+base/4 {
			t.Errorf("attempt %d: wait %v, want %v to %v", attempt, got, base, base+base/4)
		}
	}
	if got := client.retryWait(http.Header{}, 9); got != 250*time.Millisecond {
		t.Errorf("a long wait is capped at MaxWait: %v", got)
	}
	if got := client.retryWait(http.Header{"Retry-After": {"1"}}, 0); got != 250*time.Millisecond {
		t.Errorf("Retry-After of 1s under a MaxWait of 250ms: %v", got)
	}
	if got := client.retryWait(http.Header{"Retry-After-Ms": {"20"}}, 3); got != 20*time.Millisecond {
		t.Errorf("retry-after-ms wins over the backoff: %v", got)
	}
}

// The restarts of a cut stream count against the same Max, and Retry{} turns
// them off.
func TestRetryMaxBoundsTheRestartsOfACutStream(t *testing.T) {
	for _, max := range []int{0, 1, 2} {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			dropConnection(t, w, "")
		}))
		retry := Retry{}
		if max > 0 {
			retry = Retry{Max: max, Backoff: time.Millisecond, MaxWait: time.Second}
		}
		client := chatClientAt(t, server.URL, WithRetry(retry))
		stream, err := client.Stream(context.Background(), gotato.ModelRequest{})
		if err == nil {
			_, err = collectEvents(t, stream)
			_ = stream.Close()
		}
		var gerr *Error
		if !errors.As(err, &gerr) || !gerr.Retryable() || gerr.Message != streamInterruptedMessage || int(calls.Load()) != max+1 {
			t.Errorf("Max %d: err = %v, calls = %d, want %d", max, err, calls.Load(), max+1)
		}
		server.Close()
	}
}
