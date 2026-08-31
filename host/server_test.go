package host

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/internal/testmodel"
	"github.com/jinhuang712/gotato/orchestration"
)

func newTestHTTPServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	o := orchestration.New()
	if err := o.Register(orchestration.Definition{Name: "default", New: func(ctx context.Context, request orchestration.Request, snapshot *gotato.CoreSnapshot) (gotato.Agent, error) {
		options := []gotato.Option{gotato.WithModel(testmodel.EchoModel{})}
		if snapshot != nil {
			options = append(options, gotato.WithInitialSnapshot(*snapshot))
		}
		return gotato.NewAgent(options...)
	}}); err != nil {
		t.Fatal(err)
	}
	s := NewServer(o)
	return s, httptest.NewServer(s.Handler())
}

func TestHTTPRunRetireAndRehydrate(t *testing.T) {
	hostServer, server := newTestHTTPServer(t)
	defer server.Close()
	defer hostServer.Drain(context.Background())

	first := postJSON(t, server.URL+"/v1/runs", `{"agent_name":"default","conversation_key":"http-test","prompt":"one"}`)
	if first["status"] != string(gotato.RunCompleted) {
		t.Fatalf("first response = %+v", first)
	}
	conversationID := first["conversation_id"].(string)
	firstAgent := first["agent_id"].(string)

	retired := postJSON(t, server.URL+"/v1/conversations/"+conversationID+"/retire", `{"policy":"retain"}`)
	if retired["status"] != string(orchestration.ConversationDormant) {
		t.Fatalf("retired response = %+v", retired)
	}
	second := postJSON(t, server.URL+"/v1/runs", `{"agent_name":"default","conversation_key":"http-test","prompt":"two"}`)
	if second["agent_id"] == firstAgent || second["agent_generation"].(float64) != 1 {
		t.Fatalf("rehydrated response = %+v", second)
	}
}

func TestHTTPStreamContainsRunTerminalEvents(t *testing.T) {
	hostServer, server := newTestHTTPServer(t)
	defer server.Close()
	defer hostServer.Drain(context.Background())

	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/runs/stream", strings.NewReader(`{"agent_name":"default","conversation_key":"sse-test","prompt":"stream"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("content-type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "event: agent_end") || !strings.Contains(text, "event: result") {
		t.Fatalf("SSE body missing terminal events: %s", text)
	}
}

func postJSON(t *testing.T, url, body string) map[string]any {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("content-type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var decoded map[string]any
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode >= 400 {
		t.Fatalf("POST %s returned %d: %+v", url, response.StatusCode, decoded)
	}
	return decoded
}
