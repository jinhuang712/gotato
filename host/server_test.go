package host

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/internal/testmodel"
	"github.com/jinhuang712/gotato/orchestration"
)

func newTestHTTPServer(t *testing.T) (*Server, *httptest.Server) {
	return newTestHTTPServerWithModel(t, testmodel.EchoModel{})
}

func newTestHTTPServerWithModel(t *testing.T, model gotato.Model) (*Server, *httptest.Server) {
	t.Helper()
	o := orchestration.New()
	if err := o.Register(orchestration.Definition{Name: "default", New: func(ctx context.Context, request orchestration.Request, snapshot *gotato.CoreSnapshot) (gotato.Agent, error) {
		options := []gotato.Option{gotato.WithModel(model)}
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
	metrics, ok := first["metrics"].(map[string]any)
	if !ok || metrics["turns"] != float64(1) || metrics["elapsed_ms"] == nil {
		t.Fatalf("run metrics = %#v", first["metrics"])
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

func TestHTTPAsyncRunCanBePolled(t *testing.T) {
	hostServer, server := newTestHTTPServerWithModel(t, blockingModel{})
	defer server.Close()
	defer hostServer.Drain(context.Background())

	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/runs/async", strings.NewReader(`{"agent_name":"default","conversation_key":"async-test","prompt":"async"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("content-type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var submitted map[string]any
	if err := json.NewDecoder(response.Body).Decode(&submitted); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("async submit status = %d, body = %+v", response.StatusCode, submitted)
	}
	runID := submitted["run_id"].(string)
	if submitted["status"] != string(gotato.RunRunning) {
		t.Fatalf("async submit response = %+v", submitted)
	}

	pollResponse, err := http.Get(server.URL + "/v1/runs/" + runID)
	if err != nil {
		t.Fatal(err)
	}
	var polled map[string]any
	if err := json.NewDecoder(pollResponse.Body).Decode(&polled); err != nil {
		pollResponse.Body.Close()
		t.Fatal(err)
	}
	pollResponse.Body.Close()
	if polled["status"] != string(gotato.RunRunning) {
		t.Fatalf("poll response = %+v", polled)
	}
	if _, ok := polled["metrics"].(map[string]any); !ok {
		t.Fatalf("poll metrics = %#v", polled["metrics"])
	}

	cancelResponse := postJSON(t, server.URL+"/v1/runs/"+runID+"/cancel", "{}")
	if cancelResponse["status"] != "cancel_requested" {
		t.Fatalf("cancel response = %+v", cancelResponse)
	}
	for attempt := 0; attempt < 20; attempt++ {
		pollResponse, err = http.Get(server.URL + "/v1/runs/" + runID)
		if err != nil {
			t.Fatal(err)
		}
		polled = make(map[string]any)
		if err := json.NewDecoder(pollResponse.Body).Decode(&polled); err != nil {
			pollResponse.Body.Close()
			t.Fatal(err)
		}
		pollResponse.Body.Close()
		if polled["status"] == string(gotato.RunCanceled) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("async run did not settle as cancelled: %+v", polled)
}

func TestHTTPAsyncRunReturnsCompletedTurnHeartbeat(t *testing.T) {
	hostServer, server := newTestHTTPServer(t)
	defer server.Close()
	defer hostServer.Drain(context.Background())

	submitted := postJSON(t, server.URL+"/v1/runs/async", `{"agent_name":"default","conversation_key":"async-heartbeat-test","prompt":"hello"}`)
	runID := submitted["run_id"].(string)
	for attempt := 0; attempt < 20; attempt++ {
		response, err := http.Get(server.URL + "/v1/runs/" + runID)
		if err != nil {
			t.Fatal(err)
		}
		var polled map[string]any
		if err := json.NewDecoder(response.Body).Decode(&polled); err != nil {
			response.Body.Close()
			t.Fatal(err)
		}
		response.Body.Close()
		if polled["status"] == string(gotato.RunCompleted) {
			if _, ok := polled["heartbeat"].(map[string]any); !ok {
				t.Fatalf("completed response heartbeat = %#v", polled["heartbeat"])
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("async run did not complete")
}

func TestHTTPRunCancel(t *testing.T) {
	hostServer, server := newTestHTTPServerWithModel(t, blockingModel{})
	defer server.Close()
	defer hostServer.Drain(context.Background())

	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/runs/stream", strings.NewReader(`{"agent_name":"default","conversation_key":"cancel-test","prompt":"cancel"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("content-type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	reader := bufio.NewReader(response.Body)
	var runID string
	for runID == "" {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var payload struct {
			Event struct {
				RunID string `json:"run_id"`
			} `json:"event"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data: "))), &payload); err != nil {
			t.Fatal(err)
		}
		runID = payload.Event.RunID
	}

	cancelRequest, err := http.NewRequest(http.MethodPost, server.URL+"/v1/runs/"+runID+"/cancel", nil)
	if err != nil {
		t.Fatal(err)
	}
	cancelResponse, err := http.DefaultClient.Do(cancelRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer cancelResponse.Body.Close()
	if cancelResponse.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(cancelResponse.Body)
		t.Fatalf("cancel status = %d, body = %s", cancelResponse.StatusCode, body)
	}

	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"status":"cancelled"`) {
		t.Fatalf("cancelled result missing: %s", body)
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

type blockingModel struct{}

func (blockingModel) Stream(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	return blockingStream{}, nil
}

type blockingStream struct{}

func (blockingStream) Recv(ctx context.Context) (gotato.ModelEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	<-ctx.Done()
	return gotato.ModelEvent{}, ctx.Err()
}

func (blockingStream) Close() error { return nil }

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
