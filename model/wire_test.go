package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/testkit"
)

// tinyWire is a wire API a team could write outside the package: a made-up
// protocol whose request is {"model","prompt"} and whose reply is one SSE data
// line per piece of text, then "END". An "error" event carries a failure.
// It is here to prove that a new wire needs no edit of the gateway: it is
// passed to New and reuses the Connection, the Retry, the restart after a cut
// and the Classifier as they are.
type tinyWire struct {
	// badHeader makes Encode return a header net/http would refuse.
	badHeader bool
}

func (w tinyWire) Encode(call Call) (*Request, error) {
	prompt := ""
	for _, message := range call.Request.Messages {
		if message.Role == gotato.RoleUser {
			prompt = gotato.TextOf(message)
		}
	}
	body, err := json.Marshal(map[string]string{"model": call.Model.Name, "prompt": prompt})
	if err != nil {
		return nil, err
	}
	header := http.Header{}
	if call.APIKey != "" {
		header.Set("X-Tiny-Key", call.APIKey)
	}
	if w.badHeader {
		header.Set("X-Tiny", "a\nb")
	}
	return &Request{Body: body, Header: header, NewDecoder: func() Decoder { return &tinyDecoder{} }}, nil
}

type tinyDecoder struct{}

var errTinyProtocol = errors.New("tiny: bad piece")

func (d *tinyDecoder) Decode(event SSEEvent) ([]gotato.ModelEvent, bool, error) {
	switch {
	case event.End:
		// The reply has no closing event of its own to tell it complete, so a
		// body that ends here was cut.
		return nil, false, nil
	case event.Name == "error":
		return nil, false, Failure{Message: event.Data, Code: "tiny_error"}
	case event.Data == "BAD":
		return nil, false, errTinyProtocol
	case event.Data == "END":
		return []gotato.ModelEvent{{Kind: gotato.ModelEventKindDone, StopReason: gotato.StopReasonEndTurn}}, true, nil
	}
	return []gotato.ModelEvent{{Kind: gotato.ModelEventKindTextDelta, Text: event.Data}}, false, nil
}

// tinyServer answers each call with the next handler; the last repeats. It
// records the headers and bodies it saw.
type tinyServer struct {
	*httptest.Server
	mu       sync.Mutex
	headers  []http.Header
	bodies   []string
	handlers []http.HandlerFunc
}

func newTinyServer(t *testing.T, handlers ...http.HandlerFunc) *tinyServer {
	t.Helper()
	s := &tinyServer{handlers: handlers}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.headers = append(s.headers, r.Header.Clone())
		s.bodies = append(s.bodies, string(body))
		handler := s.handlers[min(len(s.bodies)-1, len(s.handlers)-1)]
		s.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *tinyServer) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.bodies)
}

// tinyReply writes the pieces of a whole reply.
func tinyReply(pieces ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, piece := range pieces {
			_, _ = w.Write([]byte("data: " + piece + "\n\n"))
		}
	}
}

// tinyCut writes pieces and drops the connection.
func tinyCut(t *testing.T, pieces ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		var raw strings.Builder
		for _, piece := range pieces {
			raw.WriteString("data: " + piece + "\n\n")
		}
		dropConnection(t, w, raw.String())
	}
}

func tinyStatus(status int, message string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"message":"`+message+`"}}`, status)
	}
}

func tinyClient(t *testing.T, server *tinyServer, conn Connection, opts ...Option) *Client {
	t.Helper()
	conn.Endpoint = server.URL + "/generate"
	client, err := New(tinyWire{}, conn, gotato.ModelConfig{Name: "tiny-1", Facts: gotato.ModelFacts{Window: gotato.WindowTokens(8000)}},
		append([]Option{WithRetry(Retry{Max: 2, Backoff: time.Millisecond, MaxWait: time.Second})}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func tinyPrompt(text string) gotato.ModelRequest {
	return gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage(text)}}
}

func TestACustomWireIsDrivenByNewAndReusesTheConnection(t *testing.T) {
	server := newTinyServer(t, tinyReply("hel", "lo", "END"))
	client := tinyClient(t, server, Connection{APIKey: "secret", Headers: map[string]string{"X-Team": "a", "X-Tiny-Key": "replaced?"}, CacheKeyHeader: "session-id"})
	request := tinyPrompt("hi")
	request.CacheKey = "ck-1"
	stream, err := client.Stream(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	events, err := collectEvents(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	if textOf(events) != "hello" || events[len(events)-1].Kind != gotato.ModelEventKindDone {
		t.Fatalf("events = %+v", events)
	}
	header := server.headers[0]
	for name, want := range map[string]string{
		"X-Tiny-Key":   "secret", // the wire's credential wins over a configured header
		"X-Team":       "a",
		"Session-Id":   "ck-1",
		"Content-Type": "application/json",
		"Accept":       "text/event-stream",
		"User-Agent":   defaultUserAgent,
	} {
		if got := header.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(server.bodies[0]), &body); err != nil || body["model"] != "tiny-1" || body["prompt"] != "hi" {
		t.Fatalf("body = %s (%v)", server.bodies[0], err)
	}
}

func TestACustomWireReusesTheRetryOfARefusedRequest(t *testing.T) {
	server := newTinyServer(t, tinyStatus(429, "slow down"), tinyStatus(503, "unavailable"), tinyReply("ok", "END"))
	client := tinyClient(t, server, Connection{})
	stream, err := client.Stream(context.Background(), tinyPrompt("hi"))
	if err != nil {
		t.Fatal(err)
	}
	events, err := collectEvents(t, stream)
	if err != nil || textOf(events) != "ok" || server.calls() != 3 {
		t.Fatalf("text = %q, err = %v, calls = %d", textOf(events), err, server.calls())
	}
	// A refusal that is not retryable ends at once, with the default verdict.
	server = newTinyServer(t, tinyStatus(400, "bad prompt"))
	client = tinyClient(t, server, Connection{})
	_, err = client.Stream(context.Background(), tinyPrompt("hi"))
	var gerr *Error
	if !errors.As(err, &gerr) || gerr.StatusCode != 400 || gerr.Retryable() || server.calls() != 1 {
		t.Fatalf("err = %v, calls = %d", err, server.calls())
	}
}

func TestACustomWireIsRestartedAfterACutBeforeAnyOutput(t *testing.T) {
	server := newTinyServer(t, tinyCut(t), tinyReply("whole", "END"))
	client := tinyClient(t, server, Connection{})
	stream, err := client.Stream(context.Background(), tinyPrompt("hi"))
	if err != nil {
		t.Fatal(err)
	}
	events, err := collectEvents(t, stream)
	if err != nil || textOf(events) != "whole" || server.calls() != 2 {
		t.Fatalf("text = %q, err = %v, calls = %d", textOf(events), err, server.calls())
	}
}

func TestACustomWireIsRestartedAfterACutWithOutputWhenASessionListens(t *testing.T) {
	server := newTinyServer(t, tinyCut(t, "half an ans"), tinyReply("the whole answer", "END"))
	client := tinyClient(t, server, Connection{})
	session, err := gotato.NewSession(context.Background(), gotato.WithModel(client))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close(context.Background()) })
	recorder := testkit.RecordEvents(session)
	result, err := session.Prompt(context.Background(), gotato.UserMessage("hi"))
	if err != nil || result.Status != gotato.RunStatusCompleted || gotato.TextOf(*result.FinalMessage) != "the whole answer" {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if len(recorder.OfKind(gotato.EventKindModelRetried)) != 1 || server.calls() != 2 {
		t.Fatalf("events = %v, calls = %d", recorder.Kinds(), server.calls())
	}
}

// With no Session to drop what was delivered, a restart would repeat output, so
// the stream fails with a retryable error instead. This guard lives in the
// loop, so a new wire has it with no work.
func TestACustomWireIsNotRestartedAfterOutputWithNobodyToDropIt(t *testing.T) {
	server := newTinyServer(t, tinyCut(t, "half"), tinyReply("whole", "END"))
	client := tinyClient(t, server, Connection{})
	stream, err := client.Stream(context.Background(), tinyPrompt("hi"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	events, err := collectEvents(t, stream)
	var gerr *Error
	if !errors.As(err, &gerr) || !gerr.Retryable() || gerr.Message != streamInterruptedMessage {
		t.Fatalf("err = %v", err)
	}
	if textOf(events) != "half" || server.calls() != 1 {
		t.Fatalf("text = %q, calls = %d", textOf(events), server.calls())
	}
}

// A body that ends between events with no terminal event: the Decoder answers
// nothing for End, and the loop treats the reply as cut off.
func TestEndWithoutATerminalEventIsACutForAWireThatCannotTellItComplete(t *testing.T) {
	// Before any output, the request is sent again.
	server := newTinyServer(t, tinyReply(), tinyReply("ok", "END"))
	client := tinyClient(t, server, Connection{})
	stream, err := client.Stream(context.Background(), tinyPrompt("hi"))
	if err != nil {
		t.Fatal(err)
	}
	events, err := collectEvents(t, stream)
	if err != nil || textOf(events) != "ok" || server.calls() != 2 {
		t.Fatalf("text = %q, err = %v, calls = %d", textOf(events), err, server.calls())
	}
	// After output with nobody to drop it, it is the retryable interruption,
	// and the read error stays out of the chain: it must not read as a clean end.
	server = newTinyServer(t, tinyReply("half"))
	client = tinyClient(t, server, Connection{})
	stream, err = client.Stream(context.Background(), tinyPrompt("hi"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = collectEvents(t, stream)
	var gerr *Error
	if !errors.As(err, &gerr) || !gerr.Retryable() || errors.Is(err, io.EOF) {
		t.Fatalf("err = %v", err)
	}
	// Out of restarts, the interruption is the Run's error.
	server = newTinyServer(t, tinyReply())
	client = tinyClient(t, server, Connection{}, WithRetry(Retry{Max: 1, Backoff: time.Millisecond, MaxWait: time.Second}))
	stream, err = client.Stream(context.Background(), tinyPrompt("hi"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = collectEvents(t, stream)
	if !errors.As(err, &gerr) || !gerr.Retryable() || gerr.Message != streamInterruptedMessage || server.calls() != 2 {
		t.Fatalf("err = %v, calls = %d", err, server.calls())
	}
}

func TestADecoderErrorThatIsNotAFailureEndsTheStreamAsItIs(t *testing.T) {
	server := newTinyServer(t, tinyReply("a", "BAD", "b"))
	client := tinyClient(t, server, Connection{})
	stream, err := client.Stream(context.Background(), tinyPrompt("hi"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	events, err := collectEvents(t, stream)
	if !errors.Is(err, errTinyProtocol) {
		t.Fatalf("err = %v", err)
	}
	var gerr *Error
	if errors.As(err, &gerr) {
		t.Fatalf("a protocol error was classified: %v", err)
	}
	if textOf(events) != "a" || server.calls() != 1 {
		t.Fatalf("text = %q, calls = %d", textOf(events), server.calls())
	}
	// The error is the answer of every Recv after, never a clean end.
	if _, again := stream.Recv(context.Background()); !errors.Is(again, errTinyProtocol) {
		t.Fatalf("a later Recv = %v", again)
	}
}

func TestAWireThatReturnsAnInvalidHeaderOrNothingIsRefusedByStream(t *testing.T) {
	server := newTinyServer(t, tinyReply("END"))
	client, err := New(tinyWire{badHeader: true}, Connection{Endpoint: server.URL}, modelOf(gotato.ModelFacts{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Stream(context.Background(), tinyPrompt("hi")); err == nil || server.calls() != 0 {
		t.Fatalf("a wire header with a newline was sent: err = %v", err)
	}
	var calls atomic.Int32
	nothing := wireFunc(func(Call) (*Request, error) { calls.Add(1); return nil, nil })
	client, err = New(nothing, Connection{Endpoint: server.URL}, modelOf(gotato.ModelFacts{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Stream(context.Background(), tinyPrompt("hi")); err == nil || calls.Load() != 1 || server.calls() != 0 {
		t.Fatalf("err = %v", err)
	}
}

// wireFunc is a WireAPI of one function.
type wireFunc func(Call) (*Request, error)

func (f wireFunc) Encode(call Call) (*Request, error) { return f(call) }

// A wire that has Validate is checked by New.
type validatedWire struct{ tinyWire }

func (validatedWire) Validate() error { return errors.New("tiny: settings are wrong") }

func TestAWireWithValidateIsCheckedByNew(t *testing.T) {
	_, err := New(validatedWire{}, Connection{Endpoint: "http://127.0.0.1:1/x"}, modelOf(gotato.ModelFacts{}))
	if err == nil || !strings.Contains(err.Error(), "tiny: settings are wrong") {
		t.Fatalf("err = %v", err)
	}
}

// The Call a wire gets: the Client's model, the key and cache key, and the
// headers of the Connection and the context, which it may add to.
func TestTheCallHandsAWireTheModelTheKeyAndTheHeaders(t *testing.T) {
	var got Call
	capture := wireFunc(func(call Call) (*Request, error) {
		got = call
		return tinyWire{}.Encode(call)
	})
	server := newTinyServer(t, tinyReply("END"))
	model := gotato.ModelConfig{Name: "tiny-1", Facts: gotato.ModelFacts{Window: gotato.WindowTokens(8000), RoundReasoning: true}}
	client, err := New(capture, Connection{Endpoint: server.URL, APIKey: "secret", Headers: map[string]string{"X-Team": "a"}, CacheKeyHeader: "session-id"}, model)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithRequestHeaders(context.Background(), map[string]string{"X-Run": "r1"})
	request := tinyPrompt("hi")
	request.CacheKey = "ck"
	stream, err := client.Stream(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	_ = stream.Close()
	if got.Model.Name != "tiny-1" || !got.Model.Facts.RoundReasoning || got.APIKey != "secret" || got.CacheKey != "ck" || got.Request.CacheKey != "ck" {
		t.Fatalf("call = %+v", got)
	}
	for name, want := range map[string]string{"X-Team": "a", "X-Run": "r1", "Session-Id": "ck"} {
		if got.Header.Get(name) != want {
			t.Errorf("Call.Header[%s] = %q, want %q", name, got.Header.Get(name), want)
		}
	}
	// The key is for the wire to place: the Client puts it in no header itself.
	if got.Header.Get("Authorization") != "" || got.Header.Get("X-Tiny-Key") != "" {
		t.Errorf("Call.Header holds a credential: %v", got.Header)
	}
}
