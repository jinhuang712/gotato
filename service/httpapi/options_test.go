package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/httpapi"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// Gotato-Not-Run says nothing happened; Gotato-Retryable says sending the
// same request again may succeed. A 404 is the first and not the second.
func TestRetryableHeaderFollowsTheRuntimesAnswer(t *testing.T) {
	g := newGate()
	in := newInstance(t, session.NewMemoryStore(), "a", g, false)
	id, wait := running(t, in, g)
	resp, data := in.post(t, "/v1/sessions/"+id+"/runs", map[string]any{"prompt": "again"})
	if resp.StatusCode != http.StatusConflict || resp.Header.Get(httpapi.NotRunHeader) != "true" || resp.Header.Get(httpapi.RetryableHeader) != "true" {
		t.Fatalf("busy = %d %s, headers %v", resp.StatusCode, data, resp.Header)
	}
	close(g.release)
	wait()
	resp, data = in.post(t, "/v1/sessions/nope/runs", map[string]any{"prompt": "x"})
	if resp.StatusCode != http.StatusNotFound || resp.Header.Get(httpapi.NotRunHeader) != "true" || resp.Header.Get(httpapi.RetryableHeader) != "" {
		t.Fatalf("not found = %d %s, headers %v", resp.StatusCode, data, resp.Header)
	}
}

// Every error body carries a code; a taken ID has its own.
func TestErrorBodiesCarryDistinctCodes(t *testing.T) {
	server, _ := newServer(t)
	call(t, server, http.MethodPost, "/v1/sessions", map[string]any{"id": "fixed"})
	for _, tc := range []struct {
		name, method, path, body string
		status                   int
		code                     string
	}{
		{"taken id", http.MethodPost, "/v1/sessions", `{"id":"fixed"}`, http.StatusConflict, "already_exists"},
		{"bad json", http.MethodPost, "/v1/sessions", `{`, http.StatusBadRequest, "invalid_argument"},
		{"unknown field", http.MethodPost, "/v1/sessions", `{"nope":1}`, http.StatusBadRequest, "invalid_argument"},
	} {
		req, _ := http.NewRequest(tc.method, server.URL+tc.path, strings.NewReader(tc.body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			Code string `json:"code"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		_ = resp.Body.Close()
		if resp.StatusCode != tc.status || out.Code != tc.code {
			t.Errorf("%s = %d code %q, want %d %q", tc.name, resp.StatusCode, out.Code, tc.status, tc.code)
		}
	}
}

// The body cap is an option; the default stays 1 MiB.
func TestWithMaxBodyRaisesAndLowersTheCap(t *testing.T) {
	big := `{"agent":"echo","metadata":{"k":"` + strings.Repeat("x", 2<<20) + `"}}`
	small := `{"agent":"echo"}`
	for _, tc := range []struct {
		name   string
		option httpapi.Option
		body   string
		want   int
	}{
		{"default", nil, big, http.StatusRequestEntityTooLarge},
		{"raised", httpapi.WithMaxBody(4 << 20), big, http.StatusCreated},
		{"lowered", httpapi.WithMaxBody(10), small, http.StatusRequestEntityTooLarge},
	} {
		runner, err := service.New(service.Config{Store: session.NewMemoryStore(), Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}}})
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(httpapi.New(runner, tc.option))
		req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/sessions", strings.NewReader(tc.body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		server.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s: status %d, want %d", tc.name, resp.StatusCode, tc.want)
		}
	}
}

// A Session whose Agent was dropped answers 400 unknown agent; the run body's
// agent rebinds it, which is the way to repair it over HTTP.
func TestRunBodyAgentRepairsASessionWhoseAgentIsGone(t *testing.T) {
	store := session.NewMemoryStore()
	a := newInstance(t, store, "a", testkit.EchoModel{}, false)
	s, err := a.runner.CreateSession(t.Context(), "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	other, err := service.New(service.Config{Store: store, Agents: []service.Agent{{Name: "else", Model: testkit.EchoModel{}}}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(other))
	t.Cleanup(func() { server.Close(); _ = other.Close(t.Context()) })
	b := &instance{server: server, runner: other}
	resp, data := b.post(t, "/v1/sessions/"+s.ID()+"/runs", map[string]any{"prompt": "hi"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("gone agent = %d %s, want 400", resp.StatusCode, data)
	}
	resp, data = b.post(t, "/v1/sessions/"+s.ID()+"/runs", map[string]any{"prompt": "hi", "agent": "else"})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(data), "completed") {
		t.Fatalf("rebound run = %d %s, want 200", resp.StatusCode, data)
	}
	resp, data = b.post(t, "/v1/sessions/"+s.ID()+"/runs", map[string]any{"prompt": "hi", "agent": "missing"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad agent = %d %s, want 400", resp.StatusCode, data)
	}
}

// A forward goes only to an address the embedder allows: the address is read
// from the store, and the forward carries the caller's credentials.
func TestForwardingOnlyToAnAllowedAddress(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		store := session.NewMemoryStore()
		g := newGate()
		a := newInstance(t, store, "instance-a", g, true)
		var asked string
		b := newInstance(t, store, "instance-b", testkit.EchoModel{}, true, httpapi.WithForwarding(nil), httpapi.WithForwardAllow(func(address string) bool {
			asked = address
			return allowed
		}))
		id, wait := running(t, a, g)
		resp, data := b.post(t, "/v1/sessions/"+id+"/messages", map[string]string{"text": "hi"}, "Authorization", "Bearer secret")
		want := http.StatusConflict
		if allowed {
			want = http.StatusAccepted
		}
		if resp.StatusCode != want || asked != a.server.URL {
			t.Errorf("allowed=%v: %d %s, asked %q, want %d asked %q", allowed, resp.StatusCode, data, asked, want, a.server.URL)
		}
		close(g.release)
		wait()
	}
}
