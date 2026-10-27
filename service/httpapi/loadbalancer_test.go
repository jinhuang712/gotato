package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/httpapi"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// send makes a request with extra headers and returns the whole response.
func send(t *testing.T, server *httptest.Server, method, path string, body any, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req, err := http.NewRequest(method, server.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
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

// The session-id header is how the load balancer finds a Session's writer, so
// the handler checks that it names the Session the request is about: a
// request that carries one ID in its header and another in its path would be
// routed to one instance and served for a Session that belongs to another.
func TestSessionIDHeaderMustAgreeWithThePath(t *testing.T) {
	server, runner := newServer(t)
	created, err := runner.CreateSession(context.Background(), "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	other, err := runner.CreateSession(context.Background(), "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/v1/sessions/" + created.ID()},
		{http.MethodGet, "/v1/sessions/" + created.ID() + "/events"},
		{http.MethodGet, "/v1/sessions/" + created.ID() + "/context"},
		{http.MethodPost, "/v1/sessions/" + created.ID() + "/runs"},
		{http.MethodPost, "/v1/sessions/" + created.ID() + "/runs/stream"},
		{http.MethodPost, "/v1/sessions/" + created.ID() + "/abort"},
		{http.MethodPost, "/v1/sessions/" + created.ID() + "/fork"},
		{http.MethodPost, "/v1/sessions/" + created.ID() + "/compact"},
		{http.MethodDelete, "/v1/sessions/" + created.ID()},
	} {
		var body any
		if strings.HasSuffix(route.path, "/runs") || strings.HasSuffix(route.path, "/runs/stream") {
			body = map[string]any{"prompt": "hi"}
		}
		resp, data := send(t, server, route.method, route.path, body, map[string]string{"session-id": other.ID()})
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "session-id") {
			t.Fatalf("%s %s with a header for another Session = %d %s, want 400 naming session-id", route.method, route.path, resp.StatusCode, data)
		}
	}
	// The same ID, in any letter case of the header name, is accepted.
	resp, data := send(t, server, http.MethodGet, "/v1/sessions/"+created.ID(), nil, map[string]string{"Session-Id": created.ID()})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("matching header = %d %s", resp.StatusCode, data)
	}
	// An empty header counts as none.
	resp, data = send(t, server, http.MethodGet, "/v1/sessions/"+created.ID(), nil, map[string]string{"session-id": " "})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("blank header = %d %s", resp.StatusCode, data)
	}
	// The refused request did nothing: the Session still has no Run.
	if loaded, err := runner.Record(context.Background(), created.ID()); err != nil || len(loaded.Snapshot().Runs) != 0 {
		t.Fatalf("a refused request changed the Session: %v", err)
	}
}

// A request that creates a Session answers with the new ID in the session-id
// header, so a client can send it on its next request and let the load balancer
// hash on it. A request that names an ID in its body must agree with its header.
func TestRequestsThatCreateASessionSetTheSessionIDHeader(t *testing.T) {
	server, _ := newServer(t)

	resp, data := send(t, server, http.MethodPost, "/v1/sessions", map[string]any{}, nil)
	var created session.RecordInfo
	_ = json.Unmarshal(data, &created)
	if resp.StatusCode != http.StatusCreated || created.ID == "" || resp.Header.Get("session-id") != created.ID {
		t.Fatalf("create: %d header %q body %s", resp.StatusCode, resp.Header.Get("session-id"), data)
	}

	resp, data = send(t, server, http.MethodPost, "/v1/sessions/"+created.ID+"/fork", nil, nil)
	var fork session.RecordInfo
	_ = json.Unmarshal(data, &fork)
	if resp.StatusCode != http.StatusCreated || fork.ID == "" || resp.Header.Get("session-id") != fork.ID {
		t.Fatalf("fork: %d header %q body %s", resp.StatusCode, resp.Header.Get("session-id"), data)
	}

	// An explicit ID in the body is the Session's ID, and the header must agree.
	resp, data = send(t, server, http.MethodPost, "/v1/sessions", map[string]any{"id": "mine"}, map[string]string{"session-id": "mine"})
	if resp.StatusCode != http.StatusCreated || resp.Header.Get("session-id") != "mine" {
		t.Fatalf("create with a matching ID: %d header %q body %s", resp.StatusCode, resp.Header.Get("session-id"), data)
	}
	resp, data = send(t, server, http.MethodPost, "/v1/sessions", map[string]any{"id": "yours"}, map[string]string{"session-id": "mine"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("create with a header for another ID = %d %s, want 400", resp.StatusCode, data)
	}
	if status, _ := call(t, server, http.MethodGet, "/v1/sessions/yours", nil); status != http.StatusNotFound {
		t.Fatalf("the refused create made a Session: %d", status)
	}
}

// fullServer returns a server whose Runner holds one Session in a held Run and
// refuses to activate a second.
func fullServer(t *testing.T) (*httptest.Server, *service.Runner, func()) {
	t.Helper()
	gate := make(chan struct{})
	model := testkit.NewFakeModel(testkit.Text("done"))
	model.Block = gate
	runner, err := service.New(service.Config{
		Store:     session.NewMemoryStore(),
		Agents:    []service.Agent{{Name: "gated", Model: model}},
		Admission: service.Admission{MaxActiveSessions: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(runner))
	t.Cleanup(server.Close)
	held := make(chan struct{})
	go func() {
		defer close(held)
		_, _ = runner.Run(context.Background(), service.RunRequest{Prompt: "hold"})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for model.Calls() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if model.Calls() == 0 {
		t.Fatal("the held Run did not reach the model")
	}
	release := func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
		<-held
	}
	t.Cleanup(release)
	return server, runner, release
}

// A full instance answers 503 and marks it "not run", in a header and in the
// body, so a load balancer retries it on another instance without running it
// twice. It is told apart from the other 503, a store failure, which may have
// run.
func TestActivationLimitAnswers503MarkedNotRun(t *testing.T) {
	server, runner, _ := fullServer(t)
	before, _ := runner.Sessions(context.Background())

	// A stored idle Session is refused; nothing is activated.
	idle, err := runner.CreateSession(context.Background(), "gated", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, data := send(t, server, http.MethodPost, "/v1/sessions/"+idle.ID()+"/runs", map[string]any{"prompt": "wake"}, map[string]string{"session-id": idle.ID()})
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Gotato-Not-Run") != "true" {
		t.Fatalf("an idle Session on a full instance = %d, header %q: %s", resp.StatusCode, resp.Header.Get("Gotato-Not-Run"), data)
	}
	var body struct {
		Code   string `json:"code"`
		NotRun bool   `json:"not_run"`
	}
	if err := json.Unmarshal(data, &body); err != nil || body.Code != "overloaded" || !body.NotRun {
		t.Fatalf("body = %s", data)
	}
	after, _ := runner.Sessions(context.Background())
	if len(after) != len(before)+1 {
		t.Fatalf("Sessions before %d, after %d: the refusals created one", len(before), len(after))
	}

	// A streaming request is refused before the stream starts, with the same
	// 503 and mark, so a load balancer can retry it on another instance.
	for _, path := range []string{"/v1/sessions/" + idle.ID() + "/runs/stream"} {
		resp, data = send(t, server, http.MethodPost, path, map[string]any{"prompt": "new"}, nil)
		if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Gotato-Not-Run") != "true" || !strings.Contains(string(data), `"not_run":true`) {
			t.Fatalf("a streamed refusal on %s = %d %s, header %q", path, resp.StatusCode, data, resp.Header.Get("Gotato-Not-Run"))
		}
		if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
			t.Fatalf("%s started a stream", path)
		}
	}

	// Other 503s are not marked: a refusal for any other reason keeps it off.
	resp, _ = send(t, server, http.MethodGet, "/readyz", nil, nil)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Gotato-Not-Run") != "" {
		t.Fatalf("readyz on a full instance = %d, mark %q; a full instance stays ready for the Sessions it holds", resp.StatusCode, resp.Header.Get("Gotato-Not-Run"))
	}
}

// /readyz reports a drain with 503, and reports how many Sessions the
// instance has active, so an operator sees how close it is to its limit.
func TestReadyzReportsActiveSessions(t *testing.T) {
	server, _, _ := fullServer(t)
	resp, data := send(t, server, http.MethodGet, "/readyz", nil, nil)
	var body struct {
		OK             bool `json:"ok"`
		ActiveRuns     int  `json:"active_runs"`
		ActiveSessions int  `json:"active_sessions"`
	}
	if err := json.Unmarshal(data, &body); err != nil || resp.StatusCode != http.StatusOK || !body.OK || body.ActiveRuns != 1 || body.ActiveSessions != 1 {
		t.Fatalf("readyz = %d %s", resp.StatusCode, data)
	}
}
