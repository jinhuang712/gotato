package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/testkit"
)

// A stream cut after some output goes through the same retry as one cut before
// any: the same request is sent again, a model_retried Event says so, and the
// new reply is the whole reply. What was received before the cut stays in the
// Record as a log, and no model is sent it.

func chatText(text string) string {
	return `data: {"choices":[{"delta":{"content":"` + text + `"}}]}` + "\n\n"
}

const chatStop = "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"

// cutServer answers each call with the next script. A script ending in a cut
// drops the connection after its chunks; any other is a whole reply. The
// last script repeats. It records the body of every request.
type cutServer struct {
	*httptest.Server
	mu      sync.Mutex
	bodies  []string
	scripts []cutScript
}

type cutScript struct {
	chunks string
	cut    bool
}

func newCutServer(t *testing.T, scripts ...cutScript) *cutServer {
	t.Helper()
	s := &cutServer{scripts: scripts}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.bodies = append(s.bodies, string(body))
		script := s.scripts[min(len(s.bodies)-1, len(s.scripts)-1)]
		s.mu.Unlock()
		if script.cut {
			dropConnection(t, w, script.chunks)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(script.chunks))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *cutServer) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.bodies...)
}

func cutSession(t *testing.T, s *cutServer, maxRetries int, opts ...gotato.Option) (*gotato.Session, *testkit.EventRecorder) {
	t.Helper()
	client, err := New(Config{Endpoint: s.URL, Model: "m", MaxRetries: maxRetries, RetryBackoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	session, err := gotato.NewSession(append([]gotato.Option{gotato.WithModel(client)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close(context.Background()) })
	return session, testkit.RecordEvents(session)
}

func TestCutAfterTextIsRetriedAndTheRunCompletes(t *testing.T) {
	server := newCutServer(t,
		cutScript{chunks: chatText("half an answ"), cut: true},
		cutScript{chunks: chatText("the whole answer") + chatStop},
	)
	session, recorder := cutSession(t, server, 2)
	result, err := session.Prompt(context.Background(), gotato.UserMessage("hi"))
	if err != nil || result.Status != gotato.RunCompleted || gotato.TextOf(*result.FinalMessage) != "the whole answer" {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if len(recorder.OfKind(gotato.KindModelRetried)) != 1 {
		t.Fatalf("model_retried Events: %v", recorder.Kinds())
	}
	cut := recorder.OfKind(gotato.KindReplyCutOff)
	if len(cut) != 1 || gotato.TextOf(cut[0].(gotato.ReplyCutOff).Message) != "half an answ" {
		t.Fatalf("the cut-off reply is not kept as a log: %v", recorder.Kinds())
	}
	// The order a reader sees: the retry first, so it drops what it had.
	order := []gotato.EventKind{}
	for _, kind := range recorder.Kinds() {
		switch kind {
		case gotato.KindModelRetried, gotato.KindReplyCutOff, gotato.KindReplyStarted, gotato.KindReplyEnded:
			order = append(order, kind)
		}
	}
	want := []gotato.EventKind{gotato.KindReplyStarted, gotato.KindModelRetried, gotato.KindReplyCutOff, gotato.KindReplyStarted, gotato.KindReplyEnded}
	if strings.Join(kindStrings(order), ",") != strings.Join(kindStrings(want), ",") {
		t.Fatalf("events = %v, want %v", order, want)
	}
	requests := server.requests()
	if len(requests) != 2 || requests[0] != requests[1] {
		t.Fatalf("the request was not sent again unchanged: %q", requests)
	}
	// The cut-off stays in the Transcript as a log only; the next request
	// does not carry it.
	if _, err := session.Prompt(context.Background(), gotato.UserMessage("more")); err != nil {
		t.Fatal(err)
	}
	next := server.requests()[2]
	if strings.Contains(next, "half an answ") || !strings.Contains(next, "the whole answer") {
		t.Fatalf("the next request = %s", next)
	}
	cutOffs := 0
	for _, message := range session.Transcript().Messages() {
		if message.StopReason == gotato.StopCutOff {
			cutOffs++
		}
	}
	if cutOffs != 1 {
		t.Fatalf("cut-off replies in the Transcript = %d, want 1", cutOffs)
	}
}

func kindStrings(kinds []gotato.EventKind) []string {
	out := make([]string, len(kinds))
	for i, kind := range kinds {
		out[i] = string(kind)
	}
	return out
}

func TestCutInsideAToolCallsArgumentsIsRetried(t *testing.T) {
	call := func(arguments string) string {
		return `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","function":{"name":"echo","arguments":` + arguments + `}}]}}]}` + "\n\n"
	}
	server := newCutServer(t,
		cutScript{chunks: chatText("checking") + call(`"{\"va"`), cut: true},
		cutScript{chunks: chatText("checking") + call(`"{}"`) + "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"},
		cutScript{chunks: chatText("done") + chatStop},
	)
	tool := testkit.NewFakeTool("echo", "echoed")
	session, recorder := cutSession(t, server, 2, gotato.WithTool(tool))
	result, err := session.Prompt(context.Background(), gotato.UserMessage("hi"))
	if err != nil || result.Status != gotato.RunCompleted || gotato.TextOf(*result.FinalMessage) != "done" {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if len(recorder.OfKind(gotato.KindModelRetried)) != 1 {
		t.Fatalf("model_retried Events: %v", recorder.Kinds())
	}
	if uses := tool.Uses(); len(uses) != 1 {
		t.Fatalf("the tool ran %d times, want once: %v", len(uses), uses)
	}
	requests := server.requests()
	if len(requests) != 3 || requests[0] != requests[1] {
		t.Fatalf("requests = %d, the second must equal the first", len(requests))
	}
	if third := requests[2]; strings.Count(third, "call-1") != 2 || strings.Contains(third, `{\"va`) {
		t.Fatalf("the request after the tool carries the cut call: %s", third)
	}
}

func TestCutsBeyondTheLimitFailTheRunWithModelFailure(t *testing.T) {
	server := newCutServer(t, cutScript{chunks: chatText("partial"), cut: true})
	session, recorder := cutSession(t, server, 2)
	result, err := session.Prompt(context.Background(), gotato.UserMessage("hi"))
	if err == nil || result.Status != gotato.RunFailed || result.Error == nil || result.Error.Code != gotato.ErrModelFailure {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if got := len(server.requests()); got != 3 {
		t.Fatalf("requests = %d, want 1 + MaxRetries", got)
	}
	if got := len(recorder.OfKind(gotato.KindModelRetried)); got != 2 {
		t.Fatalf("model_retried Events = %d, want 2", got)
	}
	// Two cut-off replies were retried; the last one fails the Run.
	if got := len(recorder.OfKind(gotato.KindReplyCutOff)); got != 3 {
		t.Fatalf("reply_cut_off Events = %d, want 3: %v", got, recorder.Kinds())
	}
}

func TestCutAfterOutputIsNotRetriedWhenTheRunIsCancelled(t *testing.T) {
	sent := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(chatText("partial")))
		w.(http.Flusher).Flush()
		once.Do(func() { close(sent) })
		<-r.Context().Done()
	}))
	defer server.Close()
	client, err := New(Config{Endpoint: server.URL, Model: "m", MaxRetries: 5, RetryBackoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	session, err := gotato.NewSession(gotato.WithModel(client))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	recorder := testkit.RecordEvents(session)
	go func() {
		<-sent
		session.Abort()
	}()
	result, _ := session.Prompt(context.Background(), gotato.UserMessage("hi"))
	if result.Status != gotato.RunCanceled {
		t.Fatalf("status = %q, want canceled", result.Status)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 || len(recorder.OfKind(gotato.KindModelRetried)) != 0 {
		t.Fatalf("a cancelled Run was retried: %d calls, %v", calls, recorder.Kinds())
	}
}

func TestInterruptDuringStreamingIsNotRetriedAsACut(t *testing.T) {
	sent := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if n > 1 {
			_, _ = w.Write([]byte(chatText("after the interrupt") + chatStop))
			return
		}
		_, _ = w.Write([]byte(chatText("partial")))
		w.(http.Flusher).Flush()
		once.Do(func() { close(sent) })
		<-r.Context().Done()
	}))
	defer server.Close()
	client, err := New(Config{Endpoint: server.URL, Model: "m", MaxRetries: 5, RetryBackoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	session, err := gotato.NewSession(gotato.WithModel(client))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	recorder := testkit.RecordEvents(session)
	go func() {
		<-sent
		_ = session.Interrupt(gotato.UserMessage("stop that"))
	}()
	result, err := session.Prompt(context.Background(), gotato.UserMessage("hi"))
	if err != nil || result.Status != gotato.RunCompleted {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 || len(recorder.OfKind(gotato.KindModelRetried)) != 0 {
		t.Fatalf("an Interrupt was retried as a cut: %d calls, %v", calls, recorder.Kinds())
	}
}

// A reasoning block that was cut is void too: the reply that follows is the
// whole reply.
func TestCutAfterReasoningIsRetried(t *testing.T) {
	reasoning := `data: {"choices":[{"delta":{"reasoning_content":"thinking"}}]}` + "\n\n"
	server := newCutServer(t,
		cutScript{chunks: reasoning, cut: true},
		cutScript{chunks: chatText("answer") + chatStop},
	)
	session, recorder := cutSession(t, server, 2)
	result, err := session.Prompt(context.Background(), gotato.UserMessage("hi"))
	if err != nil || result.Status != gotato.RunCompleted || gotato.TextOf(*result.FinalMessage) != "answer" {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if len(recorder.OfKind(gotato.KindModelRetried)) != 1 || strings.Contains(gotato.ReasoningOf(*result.FinalMessage), "thinking") {
		t.Fatalf("events %v, final %+v", recorder.Kinds(), result.FinalMessage)
	}
}
