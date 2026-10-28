package httpapi_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
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

// hangingModel streams nothing until its call ends, as a slow provider does.
type hangingModel struct{}

func (hangingModel) Stream(context.Context, gotato.ModelRequest) (gotato.ModelStream, error) {
	return hangingStream{}, nil
}

type hangingStream struct{}

func (hangingStream) Recv(ctx context.Context) (gotato.ModelEvent, error) {
	<-ctx.Done()
	return gotato.ModelEvent{}, ctx.Err()
}

func (hangingStream) Close() error { return nil }

// A client that disconnects mid-stream still gets its Run settled and saved
// (GOALS.md G-G02).
func TestStreamDisconnectSettlesAndSavesTheRun(t *testing.T) {
	runner, err := service.New(service.Config{Store: session.NewMemoryStore(), Agents: []service.Agent{{Name: "slow", Model: hangingModel{}}}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(runner))
	t.Cleanup(server.Close)
	ctx := context.Background()
	s, err := runner.CreateSession(ctx, "slow", nil)
	if err != nil {
		t.Fatal(err)
	}

	clientCtx, disconnect := context.WithCancel(ctx)
	defer disconnect()
	request, err := http.NewRequestWithContext(clientCtx, http.MethodPost, server.URL+"/v1/sessions/"+s.ID()+"/runs/stream", bytes.NewReader([]byte(`{"prompt":"hello?"}`)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	lines := bufio.NewScanner(response.Body)
	for lines.Scan() {
		if strings.TrimSpace(lines.Text()) == "event: context_built" {
			break
		}
	}
	disconnect()
	_ = response.Body.Close()

	deadline := time.Now().Add(5 * time.Second)
	for {
		view, err := runner.Record(ctx, s.ID())
		if err != nil {
			t.Fatal(err)
		}
		runs := view.Runs()
		if len(runs) == 1 && runs[0].Status != gotato.RunStatusRunning && runner.ActiveRuns() == 0 {
			if runs[0].Status != gotato.RunStatusCanceled {
				t.Fatalf("run = %+v, want canceled", runs[0])
			}
			if messages := view.Messages(); len(messages) != 1 || gotato.TextOf(messages[0]) != "hello?" {
				t.Fatalf("messages = %+v, want the prompt kept", messages)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the Run did not settle and save after the client left: runs %+v, active %d", runs, runner.ActiveRuns())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// gatedModel holds its reply until its gate opens.
type gatedModel struct {
	gate, started chan struct{}
}

func (m gatedModel) Stream(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	close(m.started)
	select {
	case <-m.gate:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return hangingModel{}.Stream(ctx, request)
}

// A Run on a Session that runs on another instance waits, then answers 409
// with Retry-After.
func TestBusyOnAnotherInstanceAnswers409WithRetryAfter(t *testing.T) {
	store := session.NewMemoryStore()
	held := gatedModel{gate: make(chan struct{}), started: make(chan struct{})}
	holder, err := service.New(service.Config{Store: store, Instance: "instance-a", Agents: []service.Agent{{Name: "slow", Model: held}}})
	if err != nil {
		t.Fatal(err)
	}
	other, err := service.New(service.Config{Store: store, Instance: "instance-b", Liveness: service.Liveness{Wait: 100 * time.Millisecond}, Agents: []service.Agent{{Name: "slow", Model: hangingModel{}}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		close(held.gate)
		_ = holder.Close(context.Background())
		_ = other.Close(context.Background())
	})
	ctx := context.Background()
	s, err := holder.CreateSession(ctx, "slow", nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_, _ = holder.Run(runCtx, service.RunRequest{SessionID: s.ID(), Prompt: "slow"})
	}()
	<-held.started

	server := httptest.NewServer(httpapi.New(other))
	t.Cleanup(server.Close)
	response, err := http.Post(server.URL+"/v1/sessions/"+s.ID()+"/runs", "application/json", strings.NewReader(`{"prompt":"meanwhile"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusConflict || response.Header.Get("Retry-After") == "" {
		t.Fatalf("status = %d, Retry-After = %q; want 409 with Retry-After", response.StatusCode, response.Header.Get("Retry-After"))
	}
}

// stalledWriter is a ResponseWriter whose client has stopped reading: its
// first Write blocks until the gate opens, as a full socket would.
type stalledWriter struct {
	header http.Header
	gate   chan struct{}
	mu     sync.Mutex
	body   strings.Builder
}

func (w *stalledWriter) Header() http.Header { return w.header }
func (w *stalledWriter) WriteHeader(int)     {}
func (w *stalledWriter) Flush()              {}

func (w *stalledWriter) Write(p []byte) (int, error) {
	<-w.gate
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.Write(p)
}

func (w *stalledWriter) text() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.String()
}

func endedIn(events []gotato.Event) bool {
	return slices.ContainsFunc(events, func(event gotato.Event) bool { return event.Kind() == gotato.EventKindRunEnded })
}

// An SSE client that stopped reading does not slow the Run: the Run completes
// and its Record holds every Event while the handler is stuck writing the
// first one; and when the client reads again it receives every stored Event
// once, in order.
func TestSSEClientThatStopsReadingDoesNotSlowTheRun(t *testing.T) {
	const calls = 400
	uses := make([]gotato.ToolCall, calls)
	for i := range uses {
		uses[i] = gotato.ToolCall{ID: gotato.ToolCallID(fmt.Sprintf("call-%d", i)), ToolID: "lookup", Arguments: []byte(`{}`)}
	}
	runner, err := service.New(service.Config{
		Store: session.NewMemoryStore(),
		Agents: []service.Agent{{
			Name:  "bulk",
			Model: testkit.NewFakeModel(testkit.ToolCalls(uses...), testkit.Text("done")),
			Tools: []gotato.Tool{testkit.NewFakeTool("lookup", "found")},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s, err := runner.CreateSession(ctx, "bulk", nil)
	if err != nil {
		t.Fatal(err)
	}

	w := &stalledWriter{header: http.Header{}, gate: make(chan struct{})}
	request := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+s.ID()+"/runs/stream", strings.NewReader(`{"prompt":"go"}`))
	request.Header.Set("Content-Type", "application/json")
	served := make(chan struct{})
	go func() {
		defer close(served)
		httpapi.New(runner).ServeHTTP(w, request)
	}()

	// The Run completes, and every Event is in the Record, with the handler
	// blocked on its first write.
	var stored []gotato.Event
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		stored, err = session.StoredEvents(ctx, runner.Store(), s.ID(), 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		if endedIn(stored) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !endedIn(stored) {
		t.Fatalf("the Run did not settle while the client was not reading: %d stored Events, body %.200q", len(stored), w.text())
	}
	select {
	case <-served:
		t.Fatal("the handler returned while its client was not reading")
	default:
	}
	counts := map[gotato.EventKind]int{}
	for _, event := range stored {
		counts[event.Kind()]++
	}
	if counts[gotato.EventKindToolStarted] != calls || counts[gotato.EventKindToolEnded] != calls || counts[gotato.EventKindToolResultCommitted] != calls {
		t.Fatalf("the Record is incomplete: %v", counts)
	}

	// The client reads again: it still gets every stored Event, once, in order.
	close(w.gate)
	select {
	case <-served:
	case <-time.After(30 * time.Second):
		t.Fatal("the handler did not finish after the client went on")
	}
	var got []string
	var last string
	scanner := bufio.NewScanner(strings.NewReader(w.text()))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if kind, ok := strings.CutPrefix(line, "event: "); ok {
			last = kind
			continue
		}
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok || last == "result" || last == "error" || last == "reconnect" {
			continue
		}
		event, err := gotato.UnmarshalEvent([]byte(data))
		if err != nil {
			t.Fatalf("bad event %q: %v", data, err)
		}
		if event.Kind().Class() == gotato.EventClassStored {
			got = append(got, fmt.Sprintf("%s#%d", event.Kind(), event.Head().Number))
		}
	}
	if last != "result" {
		t.Fatalf("the stream ended with %q, want result", last)
	}
	// The stream starts at the Run's first Event; the Record's from there on
	// is what it must equal.
	var first uint64
	if len(got) > 0 {
		_, _ = fmt.Sscanf(got[0][strings.Index(got[0], "#")+1:], "%d", &first)
	}
	var want []string
	for _, event := range stored {
		if event.Head().Number >= first {
			want = append(want, fmt.Sprintf("%s#%d", event.Kind(), event.Head().Number))
		}
		// The stream ends at the Run's run_ended.
		if event.Kind() == gotato.EventKindRunEnded {
			break
		}
	}
	if len(got) < 3*calls || !slices.Equal(got, want) {
		diff := 0
		for diff < len(got) && diff < len(want) && got[diff] == want[diff] {
			diff++
		}
		t.Fatalf("the SSE stream's stored Events differ from the Record's: %d against %d, first difference at %d", len(got), len(want), diff)
	}
}

// A Run refused at capacity is known before any byte is written, so the
// streaming routes answer it as the run route does, 429, and not with a 200 and an
// error in the stream. The refusal is marked not run, as the runtime says: no
// Run started, so another instance may take it.
func TestStreamingRoutesAnswerCapacityBeforeTheStream(t *testing.T) {
	gate := make(chan struct{})
	model := testkit.NewFakeModel(testkit.Text("done"))
	model.Block = gate
	runner, err := service.New(service.Config{
		Store:     session.NewMemoryStore(),
		Agents:    []service.Agent{{Name: "gated", Model: model}},
		Admission: service.Admission{MaxActiveRuns: 1},
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
	t.Cleanup(func() { close(gate); <-held })
	deadline := time.Now().Add(10 * time.Second)
	for model.Calls() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the held Run did not reach the model")
		}
		time.Sleep(5 * time.Millisecond)
	}
	fresh, err := runner.CreateSession(context.Background(), "gated", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/sessions/" + fresh.ID() + "/runs", "/v1/sessions/" + fresh.ID() + "/runs/stream"} {
		resp, data := send(t, server, http.MethodPost, path, map[string]any{"prompt": "new"}, nil)
		if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get(httpapi.NotRunHeader) != "true" {
			t.Fatalf("%s at capacity = %d %s, mark %q; want 429, marked not run", path, resp.StatusCode, data, resp.Header.Get(httpapi.NotRunHeader))
		}
		if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
			t.Fatalf("%s started a stream", path)
		}
	}
}

// drainGate answers once its gate opens, and reports each call as it starts.
type drainGate struct {
	gate    chan struct{}
	started chan struct{}
}

func (m *drainGate) Stream(ctx context.Context, _ gotato.ModelRequest) (gotato.ModelStream, error) {
	m.started <- struct{}{}
	select {
	case <-m.gate:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return testkit.NewFakeModel(testkit.Text("finished")).Stream(ctx, gotato.ModelRequest{})
}

// startedModel reports each call of a hanging model as it starts.
type startedModel struct {
	hangingModel
	started chan struct{}
}

func (m startedModel) Stream(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	m.started <- struct{}{}
	return m.hangingModel.Stream(ctx, request)
}

// sseFrame is one server-sent event.
type sseFrame struct {
	event, retry, data string
}

// readFrames reads a whole SSE body into its frames.
func readFrames(body io.Reader) ([]sseFrame, error) {
	raw, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	var frames []sseFrame
	for block := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n\n") {
		var frame sseFrame
		for line := range strings.SplitSeq(block, "\n") {
			field, value, _ := strings.Cut(line, ": ")
			switch field {
			case "event":
				frame.event = value
			case "retry":
				frame.retry = value
			case "data":
				frame.data = value
			}
		}
		frames = append(frames, frame)
	}
	return frames, nil
}

// openStream starts a streaming Run and reads its frames in the background.
func openStream(t *testing.T, server *httptest.Server, path, body string) <-chan []sseFrame {
	t.Helper()
	response, err := http.Post(server.URL+path, "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	out := make(chan []sseFrame, 1)
	go func() {
		defer func() { _ = response.Body.Close() }()
		frames, _ := readFrames(response.Body)
		out <- frames
	}()
	return out
}

// TestDrainEndsStreamsInOrder: while an instance drains, /readyz answers 503
// and a new Run is refused; a Run that finishes in time ends its stream with
// its result, as always; and a Run the drain stops at its deadline ends its
// stream with its result and then a reconnect frame.
func TestDrainEndsStreamsInOrder(t *testing.T) {
	gated := &drainGate{gate: make(chan struct{}), started: make(chan struct{}, 4)}
	openGate := sync.OnceFunc(func() { close(gated.gate) })
	t.Cleanup(openGate)
	hanging := startedModel{started: make(chan struct{}, 4)}
	runner, err := service.New(service.Config{
		Store:  session.NewMemoryStore(),
		Agents: []service.Agent{{Name: "gated", Model: gated}, {Name: "hanging", Model: hanging}},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(runner))
	t.Cleanup(server.Close)
	ctx := context.Background()
	finishing, err := runner.CreateSession(ctx, "gated", nil)
	if err != nil {
		t.Fatal(err)
	}
	stopped, err := runner.CreateSession(ctx, "hanging", nil)
	if err != nil {
		t.Fatal(err)
	}
	finished := openStream(t, server, "/v1/sessions/"+finishing.ID()+"/runs/stream", `{"prompt":"finish"}`)
	cut := openStream(t, server, "/v1/sessions/"+stopped.ID()+"/runs/stream", `{"prompt":"wait"}`)
	<-gated.started
	<-hanging.started
	if status, body := call(t, server, http.MethodGet, "/readyz", nil); status != http.StatusOK {
		t.Fatalf("readyz before the drain = %d %s", status, body)
	}

	drainCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	drained := make(chan error, 1)
	go func() { drained <- runner.Drain(drainCtx) }()
	deadline := time.Now().Add(10 * time.Second)
	for !runner.Draining() {
		if time.Now().After(deadline) {
			t.Fatal("the Runner did not start draining")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if status, body := call(t, server, http.MethodGet, "/readyz", nil); status != http.StatusServiceUnavailable || !strings.Contains(string(body), `"draining":true`) {
		t.Fatalf("readyz while draining = %d %s", status, body)
	}
	// A new Run while draining is refused before the stream starts, with the
	// status a run answers: 503 marked not run, so a load balancer sends it
	// to another instance. Creating the Session still works while draining.
	fresh, err := runner.CreateSession(ctx, "gated", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/sessions/" + fresh.ID() + "/runs", "/v1/sessions/" + fresh.ID() + "/runs/stream"} {
		resp, body := send(t, server, http.MethodPost, path, map[string]any{"prompt": "new"}, nil)
		if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get(httpapi.NotRunHeader) != "true" ||
			!strings.Contains(string(body), `"code":"draining"`) || !strings.Contains(string(body), `"not_run":true`) || !strings.Contains(string(body), `"retryable":true`) {
			t.Fatalf("a new Run on %s while draining = %d %s (not run %q), want 503, draining, not run, retryable", path, resp.StatusCode, body, resp.Header.Get(httpapi.NotRunHeader))
		}
	}

	openGate()
	frames := <-finished
	if last := frames[len(frames)-1]; last.event != "result" || !strings.Contains(last.data, `"status":"completed"`) || strings.Contains(last.data, "reconnect") {
		t.Fatalf("a Run that finished in the drain ends with %+v, want its completed result", last)
	}

	if err := <-drained; err == nil {
		t.Fatal("drain = nil, want its deadline")
	}
	frames = <-cut
	if len(frames) < 2 {
		t.Fatalf("a stopped Run's stream = %+v", frames)
	}
	result, reconnect := frames[len(frames)-2], frames[len(frames)-1]
	if result.event != "result" || !strings.Contains(result.data, `"status":"canceled"`) {
		t.Fatalf("a stopped Run's result = %+v, want canceled", result)
	}
	var hint service.Reconnect
	if reconnect.event != "reconnect" || json.Unmarshal([]byte(reconnect.data), &hint) != nil || hint.RetryAfterMS <= 0 || reconnect.retry == "" {
		t.Fatalf("a stopped Run's stream ends with %+v, want a reconnect frame with a delay", reconnect)
	}
	if reconnect.retry != strconv.FormatInt(hint.RetryAfterMS, 10) {
		t.Fatalf("retry %q and retry_after_ms in %s differ", reconnect.retry, reconnect.data)
	}
	// By then its Session is released.
	loaded, err := runner.Store().Load(ctx, stopped.ID())
	if err != nil || loaded.Info.Busy {
		t.Fatalf("the stopped Run's Session = %+v, %v; want released", loaded.Info, err)
	}
}

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
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get(httpapi.NotRunHeader) != "true" {
		t.Fatalf("an idle Session on a full instance = %d, header %q: %s", resp.StatusCode, resp.Header.Get(httpapi.NotRunHeader), data)
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
		if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get(httpapi.NotRunHeader) != "true" || !strings.Contains(string(data), `"not_run":true`) {
			t.Fatalf("a streamed refusal on %s = %d %s, header %q", path, resp.StatusCode, data, resp.Header.Get(httpapi.NotRunHeader))
		}
		if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
			t.Fatalf("%s started a stream", path)
		}
	}

	// Other 503s are not marked: a refusal for any other reason keeps it off.
	resp, _ = send(t, server, http.MethodGet, "/readyz", nil, nil)
	if resp.StatusCode != http.StatusOK || resp.Header.Get(httpapi.NotRunHeader) != "" {
		t.Fatalf("readyz on a full instance = %d, mark %q; a full instance stays ready for the Sessions it holds", resp.StatusCode, resp.Header.Get(httpapi.NotRunHeader))
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
