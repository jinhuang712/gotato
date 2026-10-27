package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/httpapi"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// gate answers its first call once released, and tells when it starts.
type gate struct {
	started chan struct{}
	release chan struct{}
}

func (g *gate) Stream(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	if len(request.Messages) == 1 {
		g.started <- struct{}{}
		select {
		case <-g.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return testkit.NewFakeModel(testkit.Text("reply to "+strings.TrimSpace(testkit.UserText(request)))).Stream(ctx, request)
}

// instance is one server over a Runner, on a store it shares.
type instance struct {
	server   *httptest.Server
	runner   *service.Runner
	requests atomic.Int64
}

func newInstance(t *testing.T, store session.Store, name string, model gotato.Model, advertise bool, options ...httpapi.Option) *instance {
	t.Helper()
	in := &instance{}
	in.server = httptest.NewUnstartedServer(nil)
	address := ""
	if advertise {
		address = "http://" + in.server.Listener.Addr().String()
	}
	runner, err := service.New(service.Config{
		Store: store, Instance: name, Address: address,
		Liveness: service.Liveness{TTL: 5 * time.Second, Wait: 3 * time.Second},
		Agents:   []service.Agent{{Name: "echo", Model: model}},
	})
	if err != nil {
		t.Fatal(err)
	}
	in.runner = runner
	handler := httpapi.New(runner, options...)
	in.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		in.requests.Add(1)
		handler.ServeHTTP(w, r)
	})
	in.server.Start()
	t.Cleanup(func() {
		in.server.Close()
		_ = runner.Close(context.Background())
	})
	return in
}

func (in *instance) post(t *testing.T, path string, body any, headers ...string) (*http.Response, []byte) {
	t.Helper()
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, in.server.URL+path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out bytes.Buffer
	_, _ = out.ReadFrom(resp.Body)
	return resp, out.Bytes()
}

// running starts a Run on the instance that the model holds, and returns the
// Session and a function that waits for the Run.
func running(t *testing.T, in *instance, g *gate) (string, func() service.RunResult) {
	t.Helper()
	s, err := in.runner.CreateSession(context.Background(), "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan service.RunResult, 1)
	go func() {
		result, _ := in.runner.Run(context.Background(), service.RunRequest{SessionID: s.ID(), Prompt: "hello"})
		done <- result
	}()
	select {
	case <-g.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the Run never reached the model")
	}
	return s.ID(), func() service.RunResult {
		select {
		case result := <-done:
			return result
		case <-time.After(5 * time.Second):
			t.Fatal("the Run did not settle")
			return service.RunResult{}
		}
	}
}

func transcript(t *testing.T, in *instance, id string) string {
	t.Helper()
	record, err := in.runner.Session(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var parts []string
	for _, m := range record.Messages() {
		parts = append(parts, string(m.Role)+":"+gotato.TextOf(m))
	}
	return strings.Join(parts, "|")
}

func newGate() *gate { return &gate{started: make(chan struct{}, 4), release: make(chan struct{})} }

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

	for _, path := range []string{"/messages", "/cancel"} {
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
	if result := wait(); result.Result.Status != gotato.RunCompleted {
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
	if view, _ := b.runner.Session(context.Background(), id); view.Claim().Instance != b.runner.Instance() {
		t.Fatalf("claim = %+v, want instance-b", view.Claim())
	}
}

func TestNotWriterNamesNoAddressWhenNoneIsAdvertised(t *testing.T) {
	store := session.NewMemoryStore()
	g := newGate()
	a := newInstance(t, store, "instance-a", g, false)
	b := newInstance(t, store, "instance-b", testkit.EchoModel{}, false, httpapi.WithForwarding(nil))
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
	b := newInstance(t, store, "instance-b", testkit.EchoModel{}, true, httpapi.WithForwarding(nil))
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
	b := newInstance(t, store, "instance-b", testkit.EchoModel{}, true, httpapi.WithForwarding(nil))
	id, wait := running(t, a, g)

	resp, body := b.post(t, "/v1/sessions/"+id+"/cancel", nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("forwarded cancel = %d %s, want 202", resp.StatusCode, body)
	}
	if result := wait(); result.Result.Status != gotato.RunCanceled {
		t.Fatalf("the Run = %+v, want canceled", result.Result)
	}
}

func TestAForwardedRequestIsNeverForwardedAgain(t *testing.T) {
	store := session.NewMemoryStore()
	g := newGate()
	a := newInstance(t, store, "instance-a", g, true)
	b := newInstance(t, store, "instance-b", testkit.EchoModel{}, true, httpapi.WithForwarding(nil))
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
