package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

var fastRetry = WithRetry(Retry{Max: 2, Backoff: time.Millisecond, MaxWait: time.Second})

// Every verdict of the Client comes from its Classifier. A team replaces it
// with WithClassifier: here a 400 is made retryable, which the default never
// does.
func TestWithClassifierReplacesTheVerdictOfARefusal(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			http.Error(w, `{"error":{"message":"gateway hiccup"}}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(chatText("ok") + chatStop))
	}))
	defer server.Close()

	// The default says a 400 is final.
	if _, err := chatClientAt(t, server.URL, fastRetry).Stream(context.Background(), gotato.ModelRequest{}); err == nil || calls.Load() != 1 {
		t.Fatalf("default: err = %v, calls = %d", err, calls.Load())
	}
	calls.Store(0)

	var seen []Failure
	client := chatClientAt(t, server.URL, fastRetry, WithClassifier(func(f Failure) Verdict {
		seen = append(seen, f)
		v := DefaultClassifier(f)
		if strings.Contains(f.Message, "gateway hiccup") {
			v.Retryable = true
		}
		return v
	}))
	stream, err := client.Stream(context.Background(), gotato.ModelRequest{})
	if err != nil || calls.Load() != 3 {
		t.Fatalf("err = %v, calls = %d", err, calls.Load())
	}
	_ = stream.Close()
	if len(seen) != 2 || seen[0].Status != 400 || seen[0].Message != "gateway hiccup" {
		t.Fatalf("the classifier saw %+v", seen)
	}
}

// overflowWith wraps DefaultClassifier so a team's own wording is an overflow
// with a limit.
func overflowWith(phrase string, limit int) Classifier {
	return func(f Failure) Verdict {
		v := DefaultClassifier(f)
		if strings.Contains(strings.ToLower(f.Message), phrase) {
			v.Overflow, v.Retryable, v.Limit = true, false, limit
		}
		return v
	}
}

func TestWithClassifierMakesARefusalAnOverflow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"message":"Request_Too_Big for this model"}}`, http.StatusBadRequest)
	}))
	defer server.Close()
	_, err := chatClientAt(t, server.URL, fastRetry).Stream(context.Background(), gotato.ModelRequest{})
	var gerr *Error
	if !errors.As(err, &gerr) || gerr.ContextOverflow() {
		t.Fatalf("default: err = %v", err)
	}
	_, err = chatClientAt(t, server.URL, fastRetry, WithClassifier(overflowWith("request_too_big", 4096))).Stream(context.Background(), gotato.ModelRequest{})
	if !errors.As(err, &gerr) || !gerr.ContextOverflow() || gerr.ContextLimit() != 4096 || gerr.Retryable() {
		t.Fatalf("wrapped: err = %v, error = %+v", err, gerr)
	}
}

// An error told inside the stream is a Failure the Decoder returns; the loop
// classifies it with the Client's Classifier like any other.
func TestWithClassifierJudgesAnErrorEventInTheStream(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(chatText("partial") + `data: {"error":{"message":"input is request_too_big","type":"server_error"}}` + "\n\n"))
	}))
	defer server.Close()
	run := func(opts ...Option) (*Error, []gotato.ModelEvent) {
		t.Helper()
		stream, err := chatClientAt(t, server.URL, append([]Option{fastRetry}, opts...)...).Stream(context.Background(), gotato.ModelRequest{})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = stream.Close() }()
		events, err := collectEvents(t, stream)
		var gerr *Error
		if !errors.As(err, &gerr) {
			t.Fatalf("err = %v, want a *gateway.Error", err)
		}
		return gerr, events
	}
	// By default a server_error told in the stream is the provider's own and
	// may pass; it is not an overflow.
	gerr, events := run()
	if gerr.ContextOverflow() || textOf(events) != "partial" {
		t.Fatalf("default: error = %+v, text = %q", gerr, textOf(events))
	}
	gerr, events = run(WithClassifier(overflowWith("request_too_big", 1234)))
	if !gerr.ContextOverflow() || gerr.ContextLimit() != 1234 || gerr.Retryable() || textOf(events) != "partial" {
		t.Fatalf("wrapped: error = %+v, text = %q", gerr, textOf(events))
	}
}

// A cut stream goes to the Classifier too, as the Failure "stream interrupted"
// with the read error as its cause: a team can say a cut is final.
func TestWithClassifierJudgesACutStream(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		dropConnection(t, w, "")
	}))
	defer server.Close()
	var seen []Failure
	client := chatClientAt(t, server.URL, fastRetry, WithClassifier(func(f Failure) Verdict {
		seen = append(seen, f)
		return Verdict{}
	}))
	stream, err := client.Stream(context.Background(), gotato.ModelRequest{})
	if err == nil {
		_, err = collectEvents(t, stream)
		_ = stream.Close()
	}
	var gerr *Error
	if !errors.As(err, &gerr) || gerr.Retryable() || calls.Load() != 1 {
		t.Fatalf("err = %v, calls = %d", err, calls.Load())
	}
	if len(seen) != 1 || seen[0].Message != streamInterruptedMessage || seen[0].Status != 0 || seen[0].Err == nil {
		t.Fatalf("the classifier saw %+v", seen)
	}
}

// A request that could not be sent is a Failure with no status and the
// transport error as its cause.
func TestWithClassifierJudgesATransportFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	endpoint := server.URL
	server.Close()
	var seen []Failure
	client := chatClientAt(t, endpoint, fastRetry, WithClassifier(func(f Failure) Verdict {
		seen = append(seen, f)
		return Verdict{}
	}))
	_, err := client.Stream(context.Background(), gotato.ModelRequest{})
	var gerr *Error
	if !errors.As(err, &gerr) || gerr.Retryable() || len(seen) != 1 || seen[0].Message != transportMessage || seen[0].Err == nil {
		t.Fatalf("err = %v, seen = %+v", err, seen)
	}
	if strings.Contains(gerr.Public(), "127.0.0.1") {
		t.Fatalf("the public text names the host: %q", gerr.Public())
	}
}

// The default is DefaultClassifier: a nil classifier changes nothing.
func TestANilClassifierIsTheDefault(t *testing.T) {
	server, calls := countingServer(t, 1, http.StatusServiceUnavailable)
	client := chatClientAt(t, server.URL, fastRetry, WithClassifier(nil))
	stream, err := client.Stream(context.Background(), gotato.ModelRequest{})
	if err != nil || calls.Load() != 2 {
		t.Fatalf("err = %v, calls = %d", err, calls.Load())
	}
	_ = stream.Close()
}
