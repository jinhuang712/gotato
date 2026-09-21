package httpapi_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/httpapi"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

func newServer(t *testing.T) (*httptest.Server, *service.Runner) {
	t.Helper()
	runner, err := service.New(service.Config{
		Store: session.NewMemoryStore(),
		Specs: []service.AgentSpec{
			{Name: "echo", Model: testkit.EchoModel{}, ModelName: "echo"},
			{Name: "demo", Model: testkit.DemoModel{}, ModelName: "demo", Tools: []gotato.Tool{testkit.DemoEchoTool()}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(runner))
	t.Cleanup(server.Close)
	return server, runner
}

func call(t *testing.T, server *httptest.Server, method, path string, body any) (int, []byte) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, server.URL+path, reader)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out bytes.Buffer
	_, _ = out.ReadFrom(resp.Body)
	return resp.StatusCode, out.Bytes()
}

func TestReadyzReportsDraining(t *testing.T) {
	block := make(chan struct{})
	model := testkit.NewFakeModel(testkit.Text("slow"))
	model.Block = block
	runner, err := service.New(service.Config{
		Store: session.NewMemoryStore(),
		Specs: []service.AgentSpec{{Name: "slow", Model: model}},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(runner))
	t.Cleanup(server.Close)

	go func() { _, _ = runner.Run(context.Background(), service.RunRequest{Prompt: "hi"}) }()
	deadline := time.Now().Add(2 * time.Second)
	for model.Calls() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	drainCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = runner.Drain(drainCtx) }()
	for !runner.Draining() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	status, body := call(t, server, http.MethodGet, "/readyz", nil)
	if status != http.StatusServiceUnavailable || !strings.Contains(string(body), `"draining":true`) {
		t.Fatalf("readyz while draining = %d %s", status, body)
	}
	close(block)
}

func TestSessionLifecycleOverHTTP(t *testing.T) {
	server, _ := newServer(t)
	status, body := call(t, server, http.MethodPost, "/v1/sessions", map[string]any{"agent": "demo", "metadata": map[string]string{"tenant": "x"}})
	if status != http.StatusCreated {
		t.Fatalf("create = %d %s", status, body)
	}
	var summary session.Summary
	_ = json.Unmarshal(body, &summary)
	if summary.ID == "" || summary.Metadata[service.MetaAgent] != "demo" || summary.Metadata["tenant"] != "x" {
		t.Fatalf("summary = %+v", summary)
	}

	status, body = call(t, server, http.MethodPost, "/v1/sessions/"+summary.ID+"/runs", map[string]any{"prompt": "use-tool"})
	if status != http.StatusOK {
		t.Fatalf("run = %d %s", status, body)
	}
	var result service.RunResult
	_ = json.Unmarshal(body, &result)
	if result.Result.Status != gotato.RunCompleted || result.Result.Metrics.ToolCalls != 1 || result.Messages != 4 {
		t.Fatalf("result = %+v", result)
	}

	status, body = call(t, server, http.MethodGet, "/v1/sessions/"+summary.ID, nil)
	var doc session.Document
	_ = json.Unmarshal(body, &doc)
	if status != http.StatusOK || len(doc.Messages) != 4 || len(doc.Runs) != 1 {
		t.Fatalf("get = %d messages=%d runs=%d", status, len(doc.Messages), len(doc.Runs))
	}

	status, body = call(t, server, http.MethodGet, "/v1/sessions/"+summary.ID+"/events?kind=context_built", nil)
	if status != http.StatusOK || strings.Count(strings.TrimSpace(string(body)), "\n") != 1 {
		t.Fatalf("events = %d %q", status, body)
	}

	status, body = call(t, server, http.MethodGet, "/v1/sessions/"+summary.ID+"/context", nil)
	var report map[string]any
	_ = json.Unmarshal(body, &report)
	if status != http.StatusOK || report["prefix_hash"] == "" || report["selected_messages"] != float64(4) {
		t.Fatalf("context = %d %v", status, report)
	}

	for _, prompt := range []string{"a", "b"} {
		call(t, server, http.MethodPost, "/v1/sessions/"+summary.ID+"/runs", map[string]any{"prompt": prompt})
	}
	status, body = call(t, server, http.MethodPost, "/v1/sessions/"+summary.ID+"/compact", map[string]any{"keep": 2})
	var compact map[string]any
	_ = json.Unmarshal(body, &compact)
	if status != http.StatusOK || compact["replaced"] != true || compact["messages_after"] != float64(3) {
		t.Fatalf("compact = %d %v", status, compact)
	}

	status, body = call(t, server, http.MethodPost, "/v1/sessions/"+summary.ID+"/fork", nil)
	var fork session.Summary
	_ = json.Unmarshal(body, &fork)
	if status != http.StatusCreated || fork.ParentID != summary.ID || fork.Messages != 3 {
		t.Fatalf("fork = %d %+v", status, fork)
	}

	status, body = call(t, server, http.MethodGet, "/v1/sessions", nil)
	var list []session.Summary
	_ = json.Unmarshal(body, &list)
	if status != http.StatusOK || len(list) != 2 {
		t.Fatalf("list = %d %d", status, len(list))
	}

	status, _ = call(t, server, http.MethodDelete, "/v1/sessions/"+fork.ID, nil)
	if status != http.StatusOK {
		t.Fatalf("delete = %d", status)
	}
	status, body = call(t, server, http.MethodGet, "/v1/sessions/"+fork.ID, nil)
	if status != http.StatusNotFound || !strings.Contains(string(body), "not found") {
		t.Fatalf("get deleted = %d %s", status, body)
	}
}

func TestOneShotRunAndErrors(t *testing.T) {
	server, _ := newServer(t)
	status, body := call(t, server, http.MethodPost, "/v1/runs", map[string]any{"prompt": "hi", "metadata": map[string]string{"k": "v"}})
	var result service.RunResult
	_ = json.Unmarshal(body, &result)
	if status != http.StatusOK || result.FinalText != "echo: hi" || result.SessionID == "" || result.Agent != "echo" {
		t.Fatalf("one-shot = %d %+v", status, result)
	}
	cases := []struct {
		method, path string
		body         any
		status       int
		code         string
	}{
		{http.MethodPost, "/v1/runs", map[string]any{}, http.StatusBadRequest, ""},
		{http.MethodPost, "/v1/runs", map[string]any{"prompt": "x", "agent": "nope"}, http.StatusBadRequest, "invalid_argument"},
		{http.MethodPost, "/v1/runs", map[string]any{"prompt": "x", "bogus": 1}, http.StatusBadRequest, ""},
		{http.MethodPost, "/v1/sessions/missing/runs", map[string]any{"prompt": "x"}, http.StatusNotFound, ""},
		{http.MethodPost, "/v1/sessions/missing/cancel", nil, http.StatusConflict, "invalid_state"},
		{http.MethodPost, "/v1/runs/nope/cancel", nil, http.StatusConflict, "invalid_state"},
		{http.MethodGet, "/v1/sessions/missing/context", nil, http.StatusNotFound, ""},
	}
	for _, tc := range cases {
		status, body := call(t, server, tc.method, tc.path, tc.body)
		if status != tc.status {
			t.Errorf("%s %s = %d, want %d (%s)", tc.method, tc.path, status, tc.status, body)
		}
		if tc.code != "" && !strings.Contains(string(body), `"code":"`+tc.code+`"`) {
			t.Errorf("%s %s body = %s, want code %s", tc.method, tc.path, body, tc.code)
		}
	}
}

func TestStreamRunSSE(t *testing.T) {
	server, _ := newServer(t)
	data, _ := json.Marshal(map[string]any{"prompt": "use-tool", "agent": "demo"})
	resp, err := http.Post(server.URL+"/v1/runs/stream", "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %s", ct)
	}
	scanner := bufio.NewScanner(resp.Body)
	var events []string
	var resultLine string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			events = append(events, strings.TrimPrefix(line, "event: "))
		}
		if len(events) > 0 && events[len(events)-1] == "result" && strings.HasPrefix(line, "data: ") {
			resultLine = strings.TrimPrefix(line, "data: ")
		}
	}
	if len(events) < 5 || events[0] != "agent_start" || events[len(events)-1] != "result" {
		t.Fatalf("events = %v", events)
	}
	var result service.RunResult
	if err := json.Unmarshal([]byte(resultLine), &result); err != nil || result.Result.Status != gotato.RunCompleted {
		t.Fatalf("result = %s err=%v", resultLine, err)
	}
	found := false
	for _, kind := range events {
		if kind == "context_built" {
			found = true
		}
	}
	if !found {
		t.Fatalf("context_built not streamed: %v", events)
	}
}

func TestHealthAndAgents(t *testing.T) {
	server, _ := newServer(t)
	status, body := call(t, server, http.MethodGet, "/healthz", nil)
	if status != http.StatusOK || !strings.Contains(string(body), `"contract":"2"`) {
		t.Fatalf("healthz = %d %s", status, body)
	}
	status, body = call(t, server, http.MethodGet, "/v1/agents", nil)
	if status != http.StatusOK || !strings.Contains(string(body), `["echo","demo"]`) {
		t.Fatalf("agents = %d %s", status, body)
	}
}
