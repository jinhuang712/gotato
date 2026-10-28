package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/httpapi"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// A refusal that happens before the Run holds its Session answers on the
// streaming route as it does on the run route: the same status, the same
// headers, and no stream. The stream starts once the Run is really starting.
func TestRunStreamAnswersRefusalsAsRunDoes(t *testing.T) {
	same := func(t *testing.T, in *instance, id string, wantStatus int) {
		t.Helper()
		var want *http.Response
		var wantBody []byte
		for _, route := range []string{"/runs", "/runs/stream"} {
			resp, data := in.post(t, "/v1/sessions/"+id+route, map[string]any{"prompt": "again"})
			if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
				t.Fatalf("%s started a stream: %d %s", route, resp.StatusCode, data)
			}
			if resp.StatusCode != wantStatus {
				t.Fatalf("%s = %d %s, want %d", route, resp.StatusCode, data, wantStatus)
			}
			if want == nil {
				want, wantBody = resp, data
				continue
			}
			for _, header := range []string{httpapi.NotRunHeader, "Retry-After"} {
				if got := resp.Header.Get(header); got != want.Header.Get(header) {
					t.Fatalf("%s: header %s = %q, the run route sent %q", route, header, got, want.Header.Get(header))
				}
			}
			if string(data) != string(wantBody) {
				t.Fatalf("%s body = %s, the run route sent %s", route, data, wantBody)
			}
		}
		if want.Header.Get(httpapi.NotRunHeader) != "true" {
			t.Fatalf("the refusal is not marked not run")
		}
	}

	t.Run("busy here", func(t *testing.T) {
		g := newGate()
		in := newInstance(t, session.NewMemoryStore(), "a", g, false)
		id, wait := running(t, in, g)
		same(t, in, id, http.StatusConflict)
		close(g.release)
		wait()
	})
	t.Run("held on another instance", func(t *testing.T) {
		g := newGate()
		store := session.NewMemoryStore()
		a := newInstance(t, store, "a", g, false)
		b := newInstance(t, store, "b", testkit.NewFakeModel(testkit.Text("x")), false)
		id, wait := running(t, a, g)
		same(t, b, id, http.StatusConflict)
		close(g.release)
		wait()
	})
	t.Run("missing session", func(t *testing.T) {
		in := newInstance(t, session.NewMemoryStore(), "a", testkit.NewFakeModel(testkit.Text("x")), false)
		same(t, in, "nope", http.StatusNotFound)
	})
	t.Run("unknown agent", func(t *testing.T) {
		store := session.NewMemoryStore()
		a := newInstance(t, store, "a", testkit.NewFakeModel(testkit.Text("x")), false)
		s, err := a.runner.CreateSession(t.Context(), "echo", nil)
		if err != nil {
			t.Fatal(err)
		}
		// A deploy that dropped the Agent: a second Instance that lacks it.
		other, err := service.New(service.Config{Store: store, Agents: []service.Agent{{Name: "else", Model: testkit.NewFakeModel(testkit.Text("x"))}}})
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(httpapi.New(other))
		t.Cleanup(func() { server.Close(); _ = other.Close(t.Context()) })
		same(t, &instance{server: server, runner: other}, s.ID(), http.StatusBadRequest)
	})
}
