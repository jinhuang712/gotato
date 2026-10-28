package httpapi_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/httpapi"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

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
	deadline := time.Now().Add(10 * time.Second)
	for model.Calls() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the held Run did not reach the model")
		}
		time.Sleep(5 * time.Millisecond)
	}
	drainCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = runner.Drain(drainCtx) }()
	for !runner.Draining() {
		if time.Now().After(deadline) {
			t.Fatal("the Runner did not start draining")
		}
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
	var summary session.RecordInfo
	_ = json.Unmarshal(body, &summary)
	if summary.ID == "" || summary.Metadata[service.MetaAgent] != "demo" || summary.Metadata["tenant"] != "x" {
		t.Fatalf("summary = %+v", summary)
	}

	status, body = call(t, server, http.MethodPost, "/v1/sessions/"+summary.ID+"/runs", map[string]any{"prompt": "use-tool"})
	if status != http.StatusOK {
		t.Fatalf("run = %d %s", status, body)
	}
	var result service.RunResponse
	_ = json.Unmarshal(body, &result)
	if result.Result.Status != gotato.RunStatusCompleted || result.Result.Metrics.ToolCalls != 1 || result.Messages != 4 {
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
	lines := strings.SplitSeq(trimmed, "\n")
	for line := range lines {
		filtered, err := gotato.UnmarshalEvent([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		if filtered.Kind() != gotato.EventKindContextBuilt {
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
	status, body = call(t, server, http.MethodPost, "/v1/sessions/"+summary.ID+"/compact", map[string]any{"keep_messages": 2})
	var compact map[string]any
	_ = json.Unmarshal(body, &compact)
	if status != http.StatusOK || compact["replaced"] != true || compact["messages_after"] != float64(6) {
		t.Fatalf("compact = %d %v", status, compact)
	}

	status, body = call(t, server, http.MethodPost, "/v1/sessions/"+summary.ID+"/fork", nil)
	var fork session.RecordInfo
	_ = json.Unmarshal(body, &fork)
	if status != http.StatusCreated || fork.ParentID != summary.ID || fork.Messages != 6 {
		t.Fatalf("fork = %d %+v", status, fork)
	}

	status, body = call(t, server, http.MethodGet, "/v1/sessions", nil)
	var list []session.RecordInfo
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

func TestRunAndErrors(t *testing.T) {
	server, _ := newServer(t)
	id := newSession(t, server, "echo")
	status, body := call(t, server, http.MethodPost, "/v1/sessions/"+id+"/runs", map[string]any{"prompt": "hi"})
	var result service.RunResponse
	_ = json.Unmarshal(body, &result)
	if status != http.StatusOK || result.FinalText != "echo: hi" || result.SessionID != id || result.Agent != "echo" {
		t.Fatalf("run = %d %+v", status, result)
	}
	cases := []struct {
		method, path string
		body         any
		status       int
		code         string
	}{
		{http.MethodPost, "/v1/sessions/" + id + "/runs", map[string]any{}, http.StatusBadRequest, ""},
		{http.MethodPost, "/v1/sessions", map[string]any{"agent": "nope"}, http.StatusBadRequest, "invalid_argument"},
		{http.MethodPost, "/v1/sessions/" + id + "/runs", map[string]any{"prompt": "x", "continue": true}, http.StatusBadRequest, "invalid_argument"},
		{http.MethodPost, "/v1/sessions/" + id + "/runs", map[string]any{"prompt": "x", "bogus": 1}, http.StatusBadRequest, ""},
		{http.MethodPost, "/v1/sessions/" + id + "/runs", map[string]any{"prompt": "x", "agent": "nope"}, http.StatusBadRequest, "invalid_argument"},
		{http.MethodPost, "/v1/sessions/missing/runs", map[string]any{"prompt": "x"}, http.StatusNotFound, ""},
		{http.MethodPost, "/v1/sessions/missing/abort", nil, http.StatusNotFound, "not_found"},
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
	id := newSession(t, server, "demo")
	data, _ := json.Marshal(map[string]any{"prompt": "use-tool"})
	resp, err := http.Post(server.URL+"/v1/sessions/"+id+"/runs/stream", "application/json", bytes.NewReader(data))
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
	// A Run streams the Session's stored Events, from the claim that starts it
	// to run_ended, and then its result. The store's timings
	// are Instance Events: they are not in it.
	if len(events) < 5 || events[0] != "session_claimed" || !slices.Contains(events, "run_started") ||
		events[len(events)-2] != "run_ended" || events[len(events)-1] != "result" {
		t.Fatalf("events = %v", events)
	}
	if slices.Contains(events, "session_saved") || slices.Contains(events, "session_loaded") {
		t.Fatalf("a Run's stream carries Instance Events: %v", events)
	}
	var result service.RunResponse
	if err := json.Unmarshal([]byte(resultLine), &result); err != nil || result.Result.Status != gotato.RunStatusCompleted {
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
	if status != http.StatusOK || !strings.Contains(string(body), `"contract":"1"`) {
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
			{Name: "reader", Model: testkit.EchoModel{}, MaxEffect: gotato.ToolEffectRead},
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
	id := newSession(t, server, "echo")
	for _, timeout := range []int64{1 << 62, -1} {
		status, body := call(t, server, http.MethodPost, "/v1/sessions/"+id+"/runs", map[string]any{"prompt": "hi", "timeout_ms": timeout})
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
		if s.failFinal && entry.Kind == gotato.EventKindRunEnded {
			return true
		}
	}
	return false
}

func (s *failingSaveStore) Create(ctx context.Context, id string, entries []session.Entry, summary session.RecordInfo) error {
	if s.fail(entries) {
		return errors.New("store: disk full at /var/secret/path")
	}
	return s.Store.Create(ctx, id, entries, summary)
}

func (s *failingSaveStore) Append(ctx context.Context, id string, after uint64, entries []session.Entry, summary session.RecordInfo) error {
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

	id := newSession(t, server, "echo")
	status, body := call(t, server, http.MethodPost, "/v1/sessions/"+id+"/runs", map[string]any{"prompt": "hi"})
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

	id := newSession(t, server, "big")
	status, body := call(t, server, http.MethodPost, "/v1/sessions/"+id+"/runs", map[string]any{"prompt": "hi"})
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
	if status != http.StatusOK || out.Result.Status != "failed" || out.Result.Error.Code != "context_overflow" || strings.Contains(out.Result.Error.Message, "prompt is too long") || out.Result.Error.Message == "" {
		// The Model's text stays on the Cause; the Message is fixed.
		t.Fatalf("status = %d, body = %s", status, body)
	}
	// The same error, returned as a call's error, is 422.
	if got := httpapi.StatusFor(gotato.ErrorOf(gotato.CodeContextOverflow, "prompt is too long")); got != http.StatusUnprocessableEntity {
		t.Fatalf("StatusFor(context_overflow) = %d, want 422", got)
	}
}

// WithForwarding promises that "a forward goes only to an address
// for which allow returns true; Gotato connects to nothing else". The default
// client follows redirects, so an allowed writer that answers 307 sends the
// caller's headers (its credentials) and the body to a host allow never saw.
func TestForwardDoesNotFollowARedirectOutOfTheAllowList(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer other.Close()
	writerAddr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer writerAddr.Close()

	store := session.NewMemoryStore()
	entered, release := make(chan struct{}), make(chan struct{})
	slow := testkit.NewFuncModel(func(ctx context.Context, _ gotato.ModelRequest) (testkit.Script, error) {
		close(entered)
		select {
		case <-release:
			return testkit.Text("done"), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	a := forwardRunner(t, store, service.Agent{Name: "a", Model: slow}, func(c *service.Config) { c.Instance = "a"; c.Address = writerAddr.URL })
	b := forwardRunner(t, store, service.Agent{Name: "a", Model: testkit.EchoModel{}}, func(c *service.Config) { c.Instance = "b" })
	handlerB := httpapi.New(b, httpapi.WithForwarding(nil, func(address string) bool { return address == writerAddr.URL }))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := a.CreateSession(ctx, "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = a.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "go"}) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the writer's model call")
	}
	defer close(release)

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+s.ID()+"/messages", bytes.NewBufferString(`{"text":"hi"}`))
	req.Header.Set("X-Api-Key", "secret-of-the-caller")
	rec := httptest.NewRecorder()
	handlerB.ServeHTTP(rec, req)
	if n := elsewhere.Load(); n != 0 {
		t.Errorf("the forward reached a host outside the allow list %d time(s) (answer %d)", n, rec.Code)
	}
}

func forwardRunner(t *testing.T, store session.Store, agent service.Agent, mutate func(*service.Config)) *service.Runner {
	t.Helper()
	cfg := service.Config{Store: store, Agents: []service.Agent{agent}, Liveness: service.Liveness{TTL: time.Second, Wait: 30 * time.Millisecond}}
	mutate(&cfg)
	r, err := service.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(context.Background()) })
	return r
}

// forwardMessage is the snippet FEATURES.md (G-F26) shows an embedder that
// forwards a message it was not the writer of, with its own HTTP client.
func forwardMessage(ctx context.Context, runner *service.Runner, client *http.Client, req service.MessageRequest, forwarded bool) (*http.Response, error) {
	_, err := runner.Send(ctx, req)
	addr, ok := service.WriterOf(err)
	if !ok || addr == "" || forwarded {
		return nil, err // not a not_writer, nowhere to go, or already forwarded once
	}
	body, _ := json.Marshal(map[string]string{"kind": string(req.Kind), "text": req.Text})
	hr, _ := http.NewRequestWithContext(ctx, http.MethodPost, addr+"/v1/sessions/"+req.SessionID+"/messages", bytes.NewReader(body))
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set(httpapi.ForwardedHeader, "true") // the writer never forwards it again
	return client.Do(hr)                           // your own client, timeout and credentials
}

func TestEmbedderForwardsANotWriterMessageWithItsOwnClient(t *testing.T) {
	store := session.NewMemoryStore()
	g := newGate()
	a := newInstance(t, store, "instance-a", g, true)
	b := newInstance(t, store, "instance-b", testkit.EchoModel{}, true)
	id, wait := running(t, a, g)

	resp, err := forwardMessage(context.Background(), b.runner, http.DefaultClient, service.MessageRequest{SessionID: id, Kind: service.MessageKindSteer, Text: "via the embedder"}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("forwarded message = %d, want the writer's 202", resp.StatusCode)
	}
	close(g.release)
	wait()
}

const panicSentinel = "password=panic-sentinel"

type panickingStopper struct{}

func (panickingStopper) Stop(context.Context, gotato.TurnSnapshot) (gotato.StopDecision, error) {
	panic(panicSentinel)
}

// What an Extension panicked with does not reach the client of a Run.
func TestExtensionPanicTextDoesNotReachTheClient(t *testing.T) {
	r, err := service.New(service.Config{Store: session.NewMemoryStore(), Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}, Options: []gotato.SessionOption{gotato.WithExtension(panickingStopper{})}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(context.Background()) }()
	s, err := r.CreateSession(context.Background(), "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	httpapi.New(r).ServeHTTP(w, httptest.NewRequest("POST", "/v1/sessions/"+s.ID()+"/runs", strings.NewReader(`{"prompt":"hello"}`)))
	if !strings.Contains(w.Body.String(), "extension_failure") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), panicSentinel) {
		t.Fatalf("the panic text reached the client: %s", w.Body.String())
	}
}

// A JSON body is one value: data after it is refused, and the request does not
// run. A chunked body, which has no length to check first, is bound the same.
func TestABodyWithTrailingDataIsRefused(t *testing.T) {
	runner, err := service.New(service.Config{Store: session.NewMemoryStore(), Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runner.Close(context.Background()) }()
	rec, err := runner.CreateSession(context.Background(), "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := httpapi.New(runner, httpapi.WithMaxBody(256))
	for name, tc := range map[string]struct {
		body   string
		chunks bool
		want   int
	}{
		"a second object":                 {body: `{"prompt":"hello"}{"prompt":"again"}`, want: http.StatusBadRequest},
		"garbage":                         {body: `{"prompt":"hello"} nonsense`, want: http.StatusBadRequest},
		"white space":                     {body: "{\"prompt\":\"hello\"}\n  \n", want: http.StatusOK},
		"trailing data in a chunked body": {body: `{"prompt":"hello"}{}`, chunks: true, want: http.StatusBadRequest},
		"a chunked tail past the limit":   {body: `{"prompt":"hello"}` + strings.Repeat(" ", 512) + `x`, chunks: true, want: http.StatusRequestEntityTooLarge},
	} {
		t.Run(name, func(t *testing.T) {
			var body io.Reader = strings.NewReader(tc.body)
			if tc.chunks {
				body = io.MultiReader(strings.NewReader(tc.body)) // hides the length: ContentLength is -1
			}
			request := httptest.NewRequest("POST", "/v1/sessions/"+rec.ID()+"/runs", body)
			request.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, request)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

// {"min_tokens":N} skips a stretch estimated below N and says so; with no
// "keep_messages" the latest Run's Turns stay.
func TestCompactTakesMinTokensAndKeepsTheLatestRun(t *testing.T) {
	ctx := context.Background()
	runner, err := service.New(service.Config{Store: session.NewMemoryStore(), Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}, ModelName: "echo"}}})
	if err != nil {
		t.Fatal(err)
	}
	var id string
	for _, prompt := range []string{"one", "two", "three"} {
		result, err := runner.Run(ctx, service.RunRequest{SessionID: id, Agent: "echo", Prompt: prompt})
		if err != nil {
			t.Fatal(err)
		}
		id = result.SessionID
	}
	server := httptest.NewServer(httpapi.New(runner))
	defer server.Close()
	decode := func(body []byte) map[string]any {
		var out map[string]any
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("%v: %s", err, body)
		}
		return out
	}
	status, body := call(t, server, http.MethodPost, "/v1/sessions/"+id+"/compact", map[string]any{"min_tokens": 100000})
	skipped := decode(body)
	if status != http.StatusOK || skipped["replaced"] != false || skipped["reason"] != "below_min_tokens" || skipped["messages_after"] != float64(6) {
		t.Fatalf("below min_tokens = %d %v", status, skipped)
	}
	if status, _ = call(t, server, http.MethodPost, "/v1/sessions/"+id+"/compact", map[string]any{"min_tokens": -1}); status != http.StatusBadRequest {
		t.Fatalf("a negative min_tokens = %d, want 400", status)
	}
	status, body = call(t, server, http.MethodPost, "/v1/sessions/"+id+"/compact", map[string]any{"min_tokens": 1})
	done := decode(body)
	if status != http.StatusOK || done["replaced"] != true || done["messages_after"] != float64(6) {
		t.Fatalf("compact = %d %v", status, done)
	}
	if compaction, _ := done["compaction"].(map[string]any); compaction["segment"] != float64(1) || compaction["recap"] != "none" || compaction["replaced_messages"] != float64(4) {
		t.Fatalf("compaction = %v", done["compaction"])
	}
}

// The compact route says when it merged the oldest Segments.
func TestCompactAnswersWithTheMergeOfTheOldestSegments(t *testing.T) {
	ctx := context.Background()
	runner, err := service.New(service.Config{Store: session.NewMemoryStore(), Agents: []service.Agent{{
		Name: "echo", Model: testkit.EchoModel{}, ModelName: "echo", Compact: modelctx.CompactPolicy{SegmentBudgetTokens: 1},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	var id string
	run := func(prompts ...string) {
		for _, prompt := range prompts {
			result, err := runner.Run(ctx, service.RunRequest{SessionID: id, Agent: "echo", Prompt: prompt})
			if err != nil {
				t.Fatal(err)
			}
			id = result.SessionID
		}
	}
	server := httptest.NewServer(httpapi.New(runner))
	defer server.Close()
	compact := func() map[string]any {
		status, body := call(t, server, http.MethodPost, "/v1/sessions/"+id+"/compact", map[string]any{})
		var out map[string]any
		if err := json.Unmarshal(body, &out); err != nil || status != http.StatusOK {
			t.Fatalf("compact = %d %s (%v)", status, body, err)
		}
		return out
	}
	run("one", "two", "three")
	if first := compact(); first["replaced"] != true || first["merge"] != nil {
		t.Fatalf("the first compaction: %v", first)
	}
	run("four", "five")
	second := compact()
	merge, _ := second["merge"].(map[string]any)
	if second["replaced"] != true || merge["merged"] != true || merge["recap"] != "digest" || len(merge["replaced"].([]any)) != 2 {
		t.Fatalf("the second compaction: %v", second)
	}
}
