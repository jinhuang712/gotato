package httpapi_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
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
		Agents: []service.Agent{
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
	defer func() { _ = resp.Body.Close() }()
	var out bytes.Buffer
	_, _ = out.ReadFrom(resp.Body)
	return resp.StatusCode, out.Bytes()
}

func TestReadyzReportsDraining(t *testing.T) {
	block := make(chan struct{})
	model := testkit.NewFakeModel(testkit.Text("slow"))
	model.Block = block
	runner, err := service.New(service.Config{
		Store:  session.NewMemoryStore(),
		Agents: []service.Agent{{Name: "slow", Model: model}},
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
	if status != http.StatusOK {
		t.Fatalf("events = %d %q", status, body)
	}
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		t.Fatalf("no events returned: %q", body)
	}
	lines := strings.Split(trimmed, "\n")
	for _, line := range lines {
		filtered, err := gotato.UnmarshalEvent([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		if filtered.Kind() != gotato.KindContextBuilt {
			t.Fatalf("event kind = %q, want context_built", filtered.Kind())
		}
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
	if status != http.StatusOK || compact["replaced"] != true || compact["messages_after"] != float64(6) {
		t.Fatalf("compact = %d %v", status, compact)
	}

	status, body = call(t, server, http.MethodPost, "/v1/sessions/"+summary.ID+"/fork", nil)
	var fork session.Summary
	_ = json.Unmarshal(body, &fork)
	if status != http.StatusCreated || fork.ParentID != summary.ID || fork.Messages != 6 {
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
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %s", ct)
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var events []string
	var resultLine string
	for scanner.Scan() {
		line := scanner.Text()
		if after, ok := strings.CutPrefix(line, "event: "); ok {
			events = append(events, after)
		}
		if len(events) > 0 && events[len(events)-1] == "result" && strings.HasPrefix(line, "data: ") {
			resultLine = strings.TrimPrefix(line, "data: ")
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	// A one-shot Run streams the Session's stored Events, from the ones that
	// made the Session to run_ended, and then its result. The store's timings
	// are Instance Events: they are not in it.
	if len(events) < 5 || events[0] != "session_created" || !slices.Contains(events, "run_started") ||
		events[len(events)-2] != "run_ended" || events[len(events)-1] != "result" {
		t.Fatalf("events = %v", events)
	}
	if slices.Contains(events, "session_saved") || slices.Contains(events, "session_loaded") {
		t.Fatalf("a Run's stream carries Instance Events: %v", events)
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

func TestExplicitSessionCreateIsExclusiveAndBodyIsBounded(t *testing.T) {
	server, _ := newServer(t)

	status, _ := call(t, server, http.MethodPost, "/v1/sessions", map[string]any{"id": "fixed"})
	if status != http.StatusCreated {
		t.Fatalf("create = %d", status)
	}
	status, body := call(t, server, http.MethodPost, "/v1/sessions", map[string]any{"id": "fixed"})
	if status != http.StatusConflict || !strings.Contains(string(body), "already exists") {
		t.Fatalf("duplicate create = %d %s", status, body)
	}

	// An oversized body is rejected with 413 rather than buffered.
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/sessions", strings.NewReader(`{"agent":"echo"}`+strings.Repeat(" ", 2<<20)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body = %d", resp.StatusCode)
	}
}

func TestHealthAndAgents(t *testing.T) {
	server, _ := newServer(t)
	status, body := call(t, server, http.MethodGet, "/healthz", nil)
	if status != http.StatusOK || !strings.Contains(string(body), `"contract":"4"`) {
		t.Fatalf("healthz = %d %s", status, body)
	}
	status, body = call(t, server, http.MethodGet, "/v1/agents", nil)
	if status != http.StatusOK || !strings.Contains(string(body), `["echo","demo"]`) || !strings.Contains(string(body), `"max_effect":{}`) {
		t.Fatalf("agents = %d %s", status, body)
	}
}

func TestAgentsReportMaxEffect(t *testing.T) {
	runner, err := service.New(service.Config{
		Store: session.NewMemoryStore(),
		Agents: []service.Agent{
			{Name: "open", Model: testkit.EchoModel{}},
			{Name: "reader", Model: testkit.EchoModel{}, MaxEffect: gotato.EffectRead},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(runner))
	t.Cleanup(server.Close)
	status, body := call(t, server, http.MethodGet, "/v1/agents", nil)
	var out struct {
		Agents    []string          `json:"agents"`
		MaxEffect map[string]string `json:"max_effect"`
	}
	if err := json.Unmarshal(body, &out); err != nil || status != http.StatusOK {
		t.Fatalf("agents = %d %s (%v)", status, body, err)
	}
	if len(out.Agents) != 2 || len(out.MaxEffect) != 1 || out.MaxEffect["reader"] != "read" {
		t.Fatalf("agents = %s, want only reader capped at read", body)
	}
}

func TestRunTimeoutOutOfRangeIsRejected(t *testing.T) {
	server, _ := newServer(t)
	for _, timeout := range []int64{1 << 62, -1} {
		status, body := call(t, server, http.MethodPost, "/v1/runs", map[string]any{"prompt": "hi", "timeout_ms": timeout})
		if status != http.StatusBadRequest {
			t.Fatalf("timeout_ms=%d status = %d (%s), want 400", timeout, status, body)
		}
	}
}

// failingSaveStore fails the write that ends a Run, with a store-internal
// message.
type failingSaveStore struct {
	session.Store
	mu    sync.Mutex
	saves int
	// failFinal fails the write that ends a Run: the one holding run_ended.
	failFinal bool
}

func (s *failingSaveStore) fail(entries []session.Entry) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves++
	for _, entry := range entries {
		if s.failFinal && entry.Kind == gotato.KindRunEnded {
			return true
		}
	}
	return false
}

func (s *failingSaveStore) Create(ctx context.Context, id string, entries []session.Entry, summary session.Summary) error {
	if s.fail(entries) {
		return errors.New("store: disk full at /var/secret/path")
	}
	return s.Store.Create(ctx, id, entries, summary)
}

func (s *failingSaveStore) Append(ctx context.Context, id string, after uint64, entries []session.Entry, summary session.Summary) error {
	if s.fail(entries) {
		return errors.New("store: disk full at /var/secret/path")
	}
	return s.Store.Append(ctx, id, after, entries, summary)
}

func TestInternalErrorsDoNotLeakStoreDetails(t *testing.T) {
	runner, err := service.New(service.Config{
		Store:  &failingSaveStore{Store: session.NewMemoryStore(), failFinal: true},
		Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(runner))
	t.Cleanup(server.Close)

	status, body := call(t, server, http.MethodPost, "/v1/runs", map[string]any{"prompt": "hi"})
	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d (%s), want 500", status, body)
	}
	if strings.Contains(string(body), "disk full") || strings.Contains(string(body), "/var/secret/path") {
		t.Fatalf("response leaked store internals: %s", body)
	}
	if !strings.Contains(string(body), "Internal Server Error") {
		t.Fatalf("body = %s, want a stable message", body)
	}
}

type overflowingModel struct{}

func (overflowingModel) Stream(context.Context, gotato.ModelRequest) (gotato.ModelStream, error) {
	return nil, overflowErr{}
}

type overflowErr struct{}

func (overflowErr) Error() string         { return "prompt is too long" }
func (overflowErr) ContextOverflow() bool { return true }

func TestContextOverflowRunReportsItsCodeAndMapsTo422(t *testing.T) {
	runner, err := service.New(service.Config{
		Store:  session.NewMemoryStore(),
		Agents: []service.Agent{{Name: "big", Model: overflowingModel{}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(runner))
	t.Cleanup(server.Close)

	status, body := call(t, server, http.MethodPost, "/v1/runs", map[string]any{"prompt": "hi"})
	// A Run that fails is a finished Run: 200, with the failure in the result.
	var out struct {
		Result struct {
			Status string `json:"status"`
			Error  struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("body = %s: %v", body, err)
	}
	if status != http.StatusOK || out.Result.Status != "failed" || out.Result.Error.Code != "context_overflow" || !strings.Contains(out.Result.Error.Message, "prompt is too long") {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	// The same error, returned as a call's error, is 422.
	if got := httpapi.StatusFor(gotato.ErrorOf(gotato.ErrContextOverflow, "prompt is too long")); got != http.StatusUnprocessableEntity {
		t.Fatalf("StatusFor(context_overflow) = %d, want 422", got)
	}
}
