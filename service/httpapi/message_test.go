package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/httpapi"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

func transcript(t *testing.T, in *instance, id string) string {
	t.Helper()
	record, err := in.runner.Record(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var parts []string
	for _, m := range record.Messages() {
		parts = append(parts, string(m.Role)+":"+gotato.TextOf(m))
	}
	return strings.Join(parts, "|")
}

func TestMessageIsQueuedForTheRunInFlight(t *testing.T) {
	store := session.NewMemoryStore()
	g := newGate()
	a := newInstance(t, store, "instance-a", g, true)
	id, wait := running(t, a, g)

	resp, body := a.post(t, "/v1/sessions/"+id+"/messages", map[string]string{"kind": "steer", "text": "prefer metric"})
	var queued struct {
		SessionID string `json:"session_id"`
		MessageID string `json:"message_id"`
		Queued    bool   `json:"queued"`
	}
	if err := json.Unmarshal(body, &queued); err != nil || resp.StatusCode != http.StatusAccepted || !queued.Queued || queued.MessageID == "" || queued.SessionID != id {
		t.Fatalf("message = %d %s, want 202 queued with an ID", resp.StatusCode, body)
	}
	close(g.release)
	wait()
	if got := transcript(t, a, id); got != "user:hello|assistant:reply to hello|user:prefer metric|assistant:reply to hello\nprefer metric" {
		t.Fatalf("transcript = %q", got)
	}

	// The message Events are in the stored Events the endpoint serves.
	req, _ := http.NewRequest(http.MethodGet, a.server.URL+"/v1/sessions/"+id+"/events?kind=message_queued", nil)
	events, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = events.Body.Close() }()
	var lines bytes.Buffer
	_, _ = lines.ReadFrom(events.Body)
	if !strings.Contains(lines.String(), queued.MessageID) {
		t.Fatalf("message_queued events = %q, want %s", lines.String(), queued.MessageID)
	}
}

func TestMessageValidation(t *testing.T) {
	a := newInstance(t, session.NewMemoryStore(), "instance-a", testkit.EchoModel{}, false)
	s, _ := a.runner.CreateSession(context.Background(), "echo", nil)
	for name, body := range map[string]map[string]string{
		"bad kind": {"kind": "shout", "text": "hi"},
		"no text":  {"kind": "steer"},
	} {
		resp, out := a.post(t, "/v1/sessions/"+s.ID()+"/messages", body)
		if resp.StatusCode != http.StatusBadRequest || resp.Header.Get(httpapi.NotRunHeader) != "true" {
			t.Fatalf("%s: %d %s, want 400, not run", name, resp.StatusCode, out)
		}
	}
	if resp, _ := a.post(t, "/v1/sessions/missing/messages", map[string]string{"text": "hi"}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a message for a missing Session = %d, want 404", resp.StatusCode)
	}
}

func TestMessageForARunElsewhereAnswersNotWriter(t *testing.T) {
	store := session.NewMemoryStore()
	g := newGate()
	a := newInstance(t, store, "instance-a", g, true)
	b := newInstance(t, store, "instance-b", testkit.EchoModel{}, true)
	id, wait := running(t, a, g)

	for _, path := range []string{"/messages", "/abort"} {
		resp, body := b.post(t, "/v1/sessions/"+id+path, map[string]string{"text": "from b"})
		var failure struct {
			Code      string `json:"code"`
			NotRun    bool   `json:"not_run"`
			Retryable bool   `json:"retryable"`
			Writer    string `json:"writer"`
		}
		if err := json.Unmarshal(body, &failure); err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusConflict || failure.Code != "not_writer" || !failure.NotRun || !failure.Retryable {
			t.Fatalf("%s: %d %s, want 409 not_writer, not run, retryable", path, resp.StatusCode, body)
		}
		if resp.Header.Get(httpapi.WriterHeader) != a.server.URL || failure.Writer != a.server.URL || resp.Header.Get(httpapi.NotRunHeader) != "true" {
			t.Fatalf("%s: headers %v body %s, want the writer %s", path, resp.Header, body, a.server.URL)
		}
	}
	// The Run was not touched.
	close(g.release)
	if result := wait(); result.Result.Status != gotato.RunStatusCompleted {
		t.Fatalf("the Run = %+v, want completed", result.Result)
	}
	if got := transcript(t, a, id); got != "user:hello|assistant:reply to hello" {
		t.Fatalf("transcript = %q", got)
	}

	// With no Run going, the same instance takes the Session over and runs.
	resp, body := b.post(t, "/v1/sessions/"+id+"/messages", map[string]string{"kind": "follow_up", "text": "next"})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"queued":false`) || !strings.Contains(string(body), "echo: next") {
		t.Fatalf("message with no Run going = %d %s, want 200 with the new Run", resp.StatusCode, body)
	}
	if view, _ := b.runner.Record(context.Background(), id); view.Claim().Instance != b.runner.Instance() {
		t.Fatalf("claim = %+v, want instance-b", view.Claim())
	}
}

func TestNotWriterNamesNoAddressWhenNoneIsAdvertised(t *testing.T) {
	store := session.NewMemoryStore()
	g := newGate()
	a := newInstance(t, store, "instance-a", g, false)
	b := newInstance(t, store, "instance-b", testkit.EchoModel{}, false, httpapi.WithForwarding(nil, anyAddress))
	id, wait := running(t, a, g)

	// Forwarding is on, but there is nowhere to forward to.
	resp, body := b.post(t, "/v1/sessions/"+id+"/messages", map[string]string{"text": "from b"})
	if resp.StatusCode != http.StatusConflict || resp.Header.Get(httpapi.WriterHeader) != "" || !strings.Contains(string(body), `"code":"not_writer"`) || strings.Contains(string(body), `"writer"`) {
		t.Fatalf("%d %v %s, want 409 not_writer with no writer", resp.StatusCode, resp.Header, body)
	}
	close(g.release)
	wait()
}

func TestForwardingSendsTheMessageToTheWriter(t *testing.T) {
	store := session.NewMemoryStore()
	g := newGate()
	a := newInstance(t, store, "instance-a", g, true)
	b := newInstance(t, store, "instance-b", testkit.EchoModel{}, true, httpapi.WithForwarding(nil, anyAddress))
	id, wait := running(t, a, g)
	before := a.requests.Load()

	resp, body := b.post(t, "/v1/sessions/"+id+"/messages", map[string]string{"kind": "steer", "text": "via b"}, "X-Caller", "chat-bot")
	if resp.StatusCode != http.StatusAccepted || !strings.Contains(string(body), `"queued":true`) {
		t.Fatalf("forwarded message = %d %s, want the writer's 202", resp.StatusCode, body)
	}
	if a.requests.Load() != before+1 {
		t.Fatalf("the writer saw %d requests, want 1 forwarded", a.requests.Load()-before)
	}
	close(g.release)
	wait()
	if got := transcript(t, a, id); !strings.Contains(got, "user:via b") {
		t.Fatalf("transcript = %q, want the forwarded message", got)
	}
}

func TestForwardingStopsTheRunOnTheWriter(t *testing.T) {
	store := session.NewMemoryStore()
	g := newGate()
	a := newInstance(t, store, "instance-a", g, true)
	b := newInstance(t, store, "instance-b", testkit.EchoModel{}, true, httpapi.WithForwarding(nil, anyAddress))
	id, wait := running(t, a, g)

	resp, body := b.post(t, "/v1/sessions/"+id+"/abort", nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("forwarded cancel = %d %s, want 202", resp.StatusCode, body)
	}
	if result := wait(); result.Result.Status != gotato.RunStatusCanceled {
		t.Fatalf("the Run = %+v, want canceled", result.Result)
	}
}

func TestAForwardedRequestIsNeverForwardedAgain(t *testing.T) {
	store := session.NewMemoryStore()
	g := newGate()
	a := newInstance(t, store, "instance-a", g, true)
	b := newInstance(t, store, "instance-b", testkit.EchoModel{}, true, httpapi.WithForwarding(nil, anyAddress))
	id, wait := running(t, a, g)
	before := a.requests.Load()

	resp, body := b.post(t, "/v1/sessions/"+id+"/messages", map[string]string{"text": "again"}, httpapi.ForwardedHeader, "true")
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), `"code":"not_writer"`) {
		t.Fatalf("a forwarded request = %d %s, want 409 not_writer", resp.StatusCode, body)
	}
	if a.requests.Load() != before {
		t.Fatalf("the writer saw %d requests, want none: the request was forwarded again", a.requests.Load()-before)
	}
	close(g.release)
	wait()
}

type cannedTransport func(*http.Request) *http.Response

func (f cannedTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r), nil }

func TestForwardingDoesNotRelayHopByHopHeaders(t *testing.T) {
	store := session.NewMemoryStore()
	g := newGate()
	a := newInstance(t, store, "instance-a", g, true)
	canned := &http.Client{Transport: cannedTransport(func(r *http.Request) *http.Response {
		h := http.Header{}
		h.Set("Keep-Alive", "timeout=5")
		h.Set("Proxy-Authenticate", "Basic")
		h.Set("Connection", "X-Hop")
		h.Set("X-Hop", "1")
		h.Set("X-Kept", "yes")
		return &http.Response{StatusCode: http.StatusAccepted, Header: h, Body: io.NopCloser(strings.NewReader("{}")), Request: r}
	})}
	b := newInstance(t, store, "instance-b", testkit.EchoModel{}, true, httpapi.WithForwarding(canned, anyAddress))
	id, wait := running(t, a, g)
	resp, _ := b.post(t, "/v1/sessions/"+id+"/abort", nil)
	for _, name := range []string{"Keep-Alive", "Proxy-Authenticate", "X-Hop"} {
		if resp.Header.Get(name) != "" {
			t.Fatalf("the hop-by-hop header %s was relayed: %v", name, resp.Header)
		}
	}
	if resp.Header.Get("X-Kept") != "yes" {
		t.Fatalf("an end-to-end header was lost: %v", resp.Header)
	}
	close(g.release)
	wait()
}

// releaseGate holds the write that releases the Session, once, so a Run is held
// after run_ended, when it takes no message and still owns the Session.
type releaseGate struct {
	session.Store
	reached chan struct{}
	proceed chan struct{}
	once    sync.Once
}

func (g *releaseGate) Append(ctx context.Context, id string, after uint64, entries []session.Entry, summary session.RecordInfo) error {
	for _, entry := range entries {
		if entry.Kind == "session_released" {
			g.once.Do(func() {
				close(g.reached)
				<-g.proceed
			})
		}
	}
	return g.Store.Append(ctx, id, after, entries, summary)
}

// A message posted after run_ended and before the Run releases the Session
// waits for the release and runs in a following Run, instead of answering busy.
func TestMessageWhileARunIsEndingRunsInTheNextRun(t *testing.T) {
	gate := &releaseGate{Store: session.NewMemoryStore(), reached: make(chan struct{}), proceed: make(chan struct{})}
	a := newInstance(t, gate, "instance-a", testkit.EchoModel{}, false)
	s, err := a.runner.CreateSession(context.Background(), "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_, _ = a.runner.Run(context.Background(), service.RunRequest{SessionID: s.ID(), Prompt: "hello"})
	}()
	select {
	case <-gate.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the Run never reached its release")
	}
	type answer struct {
		status int
		body   []byte
	}
	answered := make(chan answer, 1)
	go func() {
		resp, body := a.post(t, "/v1/sessions/"+s.ID()+"/messages", map[string]string{"kind": "follow_up", "text": "again"})
		answered <- answer{resp.StatusCode, body}
	}()
	select {
	case got := <-answered:
		t.Fatalf("the message was answered before the release: %d %s", got.status, got.body)
	case <-time.After(50 * time.Millisecond):
	}
	close(gate.proceed)
	select {
	case got := <-answered:
		var out struct {
			Queued bool `json:"queued"`
			Run    *struct {
				FinalText string `json:"final_text"`
			} `json:"run"`
		}
		if err := json.Unmarshal(got.body, &out); err != nil || got.status != http.StatusOK || out.Queued || out.Run == nil || out.Run.FinalText != "echo: again" {
			t.Fatalf("message = %d %s, want 200 with a following Run answering \"echo: again\"", got.status, got.body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the message never answered")
	}
}

type noticeAnswer struct {
	SessionID string `json:"session_id"`
	Number    uint64 `json:"number"`
	MessageID string `json:"message_id"`
}

// A notice sent to a Run is answered 202 with the number of its notice_queued
// Event; it starts no Run, and goes in right before the model call of the
// Session's next Run, after its prompt.
func TestNoticeIsQueuedForTheNextModelCall(t *testing.T) {
	g := newGate()
	a := newInstance(t, session.NewMemoryStore(), "instance-a", g, true)
	id, wait := running(t, a, g)

	resp, body := a.post(t, "/v1/sessions/"+id+"/notices", map[string]string{"kind": "order_update", "text": "Order 42 was delivered."})
	var answer noticeAnswer
	if err := json.Unmarshal(body, &answer); err != nil || resp.StatusCode != http.StatusAccepted || answer.Number == 0 || answer.MessageID == "" || answer.SessionID != id {
		t.Fatalf("notice = %d %s, want 202 with a number", resp.StatusCode, body)
	}
	close(g.release)
	wait()
	// The Run settled without it: a notice keeps no Run going.
	if got := transcript(t, a, id); got != "user:hello|assistant:reply to hello" {
		t.Fatalf("transcript = %q", got)
	}
	resp, body = a.post(t, "/v1/sessions/"+id+"/runs", map[string]string{"prompt": "again"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("run = %d %s", resp.StatusCode, body)
	}
	if got := transcript(t, a, id); !strings.Contains(got, "user:again|notice:Order 42 was delivered.|assistant:") {
		t.Fatalf("transcript = %q, want the notice after the prompt", got)
	}
}

func TestNoticeValidation(t *testing.T) {
	a := newInstance(t, session.NewMemoryStore(), "instance-a", testkit.EchoModel{}, false)
	s, _ := a.runner.CreateSession(context.Background(), "echo", nil)
	for name, body := range map[string]map[string]string{
		"no kind":       {"text": "hi"},
		"a reserved":    {"kind": "time_away", "text": "hi"},
		"no text":       {"kind": "k"},
		"a blank text ": {"kind": "k", "text": "  "},
	} {
		resp, out := a.post(t, "/v1/sessions/"+s.ID()+"/notices", body)
		if resp.StatusCode != http.StatusBadRequest || resp.Header.Get(httpapi.NotRunHeader) != "true" {
			t.Fatalf("%s: %d %s, want 400, not run", name, resp.StatusCode, out)
		}
	}
	if resp, _ := a.post(t, "/v1/sessions/missing/notices", map[string]string{"kind": "k", "text": "hi"}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a notice for a missing Session = %d, want 404", resp.StatusCode)
	}
}

func TestNoticeForARunElsewhereAnswersNotWriterOrIsForwarded(t *testing.T) {
	store := session.NewMemoryStore()
	g := newGate()
	a := newInstance(t, store, "instance-a", g, true)
	plain := newInstance(t, store, "instance-b", testkit.EchoModel{}, true)
	forwarding := newInstance(t, store, "instance-c", testkit.EchoModel{}, true, httpapi.WithForwarding(nil, anyAddress))
	id, wait := running(t, a, g)

	resp, body := plain.post(t, "/v1/sessions/"+id+"/notices", map[string]string{"kind": "k", "text": "from b"})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), `"code":"not_writer"`) || resp.Header.Get(httpapi.WriterHeader) != a.server.URL || resp.Header.Get(httpapi.NotRunHeader) != "true" {
		t.Fatalf("%d %v %s, want 409 not_writer naming the writer", resp.StatusCode, resp.Header, body)
	}
	before := a.requests.Load()
	resp, body = forwarding.post(t, "/v1/sessions/"+id+"/notices", map[string]string{"kind": "k", "text": "via c"})
	if resp.StatusCode != http.StatusAccepted || !strings.Contains(string(body), `"number":`) || a.requests.Load() != before+1 {
		t.Fatalf("forwarded notice = %d %s (%d requests at the writer)", resp.StatusCode, body, a.requests.Load()-before)
	}
	close(g.release)
	wait()
}
