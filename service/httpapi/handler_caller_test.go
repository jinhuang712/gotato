package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/httpapi"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// callerKey is the private key a service puts the caller under in the
// request's context.
type callerKey struct{}

// ownerStore is the access rule of an application, as a session.Store
// wrapper: it answers not_found for a Record whose "owner" metadata is not
// the caller. It also notes which calls saw a caller in their ctx.
type ownerStore struct {
	session.Store
	mu    sync.Mutex
	calls map[string][]bool
}

func (s *ownerStore) note(ctx context.Context, op string) string {
	caller, _ := ctx.Value(callerKey{}).(string)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.calls == nil {
		s.calls = map[string][]bool{}
	}
	s.calls[op] = append(s.calls[op], caller != "")
	return caller
}

func (s *ownerStore) Create(ctx context.Context, id string, entries []session.Entry, summary session.RecordInfo) error {
	s.note(ctx, "create")
	return s.Store.Create(ctx, id, entries, summary)
}

func (s *ownerStore) Load(ctx context.Context, id string) (session.Loaded, error) {
	caller := s.note(ctx, "load")
	loaded, err := s.Store.Load(ctx, id)
	if err == nil && loaded.Info.Metadata["owner"] != caller {
		return session.Loaded{}, session.ErrNotFound // does not reveal that the Session exists
	}
	return loaded, err
}

// Log is how a read of a Session reaches the store, so it is guarded too.
func (s *ownerStore) Log(ctx context.Context, id string, after uint64, limit int) ([]session.Entry, error) {
	s.note(ctx, "log")
	if _, err := s.Load(ctx, id); err != nil {
		return nil, err
	}
	return s.Store.Log(ctx, id, after, limit)
}

func (s *ownerStore) Append(ctx context.Context, id string, after uint64, entries []session.Entry, summary session.RecordInfo) error {
	s.note(ctx, "append")
	return s.Store.Append(ctx, id, after, entries, summary)
}

func (s *ownerStore) seen(op string) []bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]bool(nil), s.calls[op]...)
}

func callAs(t *testing.T, server *httptest.Server, caller, method, path string, body any) (int, []byte) {
	t.Helper()
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, server.URL+path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Caller", caller)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out bytes.Buffer
	_, _ = out.ReadFrom(resp.Body)
	return resp.StatusCode, out.Bytes()
}

// The caller a service puts into the request's context reaches every store
// call the request makes, and a store that answers not_found for another
// caller's Session makes HTTP answer 404.
func TestRequestContextReachesTheStoreAndARefusalAnswers404(t *testing.T) {
	store := &ownerStore{Store: session.NewMemoryStore()}
	newServer := func() (*httptest.Server, *service.Runner) {
		runner, err := service.New(service.Config{
			Store:  store,
			Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}, ModelName: "echo"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		inner := httpapi.New(runner)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), callerKey{}, r.Header.Get("X-Caller"))
			inner.ServeHTTP(w, r.WithContext(ctx))
		}))
		t.Cleanup(server.Close)
		return server, runner
	}
	_, firstRunner := newServer()
	// Other instances on the same store hold nothing in memory, so their
	// requests load the Record.
	second, _ := newServer()
	third, _ := newServer()

	// The application's own endpoint creates the Session and records the
	// owner in its metadata, which Gotato keeps with the Session.
	alice := context.WithValue(context.Background(), callerKey{}, "alice")
	created, err := firstRunner.CreateSession(alice, "echo", map[string]string{"owner": "alice"})
	if err != nil {
		t.Fatal(err)
	}
	id := created.ID()

	var status int
	var body []byte
	for _, prompt := range []string{"one", "two", "three"} {
		if status, body = callAs(t, second, "alice", http.MethodPost, "/v1/sessions/"+id+"/runs", map[string]any{"prompt": prompt}); status != http.StatusOK {
			t.Fatalf("run = %d %s", status, body)
		}
	}
	if status, body = callAs(t, second, "alice", http.MethodPost, "/v1/sessions/"+id+"/compact", map[string]any{"min_tokens": 1}); status != http.StatusOK {
		t.Fatalf("compact = %d %s", status, body)
	}
	for _, op := range []string{"create", "load", "log", "append"} {
		calls := store.seen(op)
		if len(calls) == 0 {
			t.Fatalf("no %s call reached the store", op)
		}
		for i, ok := range calls {
			if !ok {
				t.Errorf("%s call %d of %d lost the caller in its ctx", op, i+1, len(calls))
			}
		}
	}

	// Another caller is refused, on an instance that loads the Record and on
	// one that ran it.
	for _, server := range []*httptest.Server{third, second} {
		for _, tc := range []struct{ name, method, path string }{
			{"read", http.MethodGet, "/v1/sessions/" + id},
			{"run", http.MethodPost, "/v1/sessions/" + id + "/runs"},
		} {
			if status, body = callAs(t, server, "mallory", tc.method, tc.path, map[string]any{"prompt": "hi"}); status != http.StatusNotFound {
				t.Errorf("%s by another caller = %d %s, want 404", tc.name, status, body)
			}
		}
	}
	// A cancel by another caller answers 404 here too, with no Run going on.
	if status, body = callAs(t, third, "mallory", http.MethodPost, "/v1/sessions/"+id+"/abort", nil); status != http.StatusNotFound {
		t.Errorf("cancel by another caller = %d %s, want 404", status, body)
	}
	// The owner still reads it.
	if status, body = callAs(t, third, "alice", http.MethodGet, "/v1/sessions/"+id, nil); status != http.StatusOK {
		t.Errorf("read by the owner = %d %s", status, body)
	}
}

// While the owner's Run is live and held on this instance, another caller
// cannot cancel it, steer it or send it a notice: each call reads through the
// store first, so the wrapper's not_found answers 404. The owner still can.
func TestARefusalAlsoGuardsALiveRun(t *testing.T) {
	store := &ownerStore{Store: session.NewMemoryStore()}
	block := make(chan struct{})
	model := testkit.NewFakeModel(testkit.Text("slow"))
	model.Block = block
	runner, err := service.New(service.Config{
		Store:  store,
		Agents: []service.Agent{{Name: "slow", Model: model}},
	})
	if err != nil {
		t.Fatal(err)
	}
	inner := httpapi.New(runner)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callerKey{}, r.Header.Get("X-Caller"))))
	}))
	defer server.Close()

	alice := context.WithValue(context.Background(), callerKey{}, "alice")
	created, err := runner.CreateSession(alice, "slow", map[string]string{"owner": "alice"})
	if err != nil {
		t.Fatal(err)
	}
	id := created.ID()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = runner.Run(alice, service.RunRequest{SessionID: id, Prompt: "hi"})
	}()
	defer func() { <-done }()
	defer close(block)
	for model.Calls() == 0 {
		runtime.Gosched()
	}

	steer := map[string]any{"text": "change course"}
	notice := map[string]any{"kind": "hint", "text": "be brief"}
	for _, tc := range []struct {
		name, path string
		body       any
	}{
		{"steer", "/messages", steer},
		{"notice", "/notices", notice},
		{"abort", "/abort", nil},
	} {
		if status, body := callAs(t, server, "mallory", http.MethodPost, "/v1/sessions/"+id+tc.path, tc.body); status != http.StatusNotFound {
			t.Errorf("%s by another caller = %d %s, want 404", tc.name, status, body)
		}
	}
	if status, body := callAs(t, server, "alice", http.MethodPost, "/v1/sessions/"+id+"/messages", steer); status >= 300 {
		t.Errorf("steer by the owner = %d %s", status, body)
	}
	if status, body := callAs(t, server, "alice", http.MethodPost, "/v1/sessions/"+id+"/notices", notice); status >= 300 {
		t.Errorf("notice by the owner = %d %s", status, body)
	}
	if status, body := callAs(t, server, "alice", http.MethodPost, "/v1/sessions/"+id+"/abort", nil); status != http.StatusAccepted {
		t.Errorf("cancel by the owner = %d %s", status, body)
	}
}

// beatOutage fails the Liveness renewals while down is set.
type beatOutage struct {
	session.Store
	down atomic.Bool
}

func (s *beatOutage) Beat(ctx context.Context, row session.InstanceRow, ttl time.Duration) error {
	if s.down.Load() {
		return errors.New("store unreachable")
	}
	return s.Store.Beat(ctx, row, ttl)
}

// A Runner that cannot show its Liveness row is alive refuses writes, so
// /readyz answers 503 until it can.
func TestReadyzReportsALapsedRow(t *testing.T) {
	store := &beatOutage{Store: session.NewMemoryStore()}
	runner, err := service.New(service.Config{
		Store:    store,
		Liveness: service.Liveness{TTL: 300 * time.Millisecond},
		Agents:   []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close(context.Background()) })
	server := httptest.NewServer(httpapi.New(runner))
	t.Cleanup(server.Close)
	if _, err := runner.CreateSession(context.Background(), "echo", nil); err != nil {
		t.Fatal(err)
	}
	if status, body := call(t, server, http.MethodGet, "/readyz", nil); status != http.StatusOK {
		t.Fatalf("readyz = %d %s, want 200", status, body)
	}
	store.down.Store(true)
	time.Sleep(600 * time.Millisecond)
	status, body := call(t, server, http.MethodGet, "/readyz", nil)
	if status != http.StatusServiceUnavailable || !strings.Contains(string(body), `"lapsed":true`) {
		t.Fatalf("readyz with a lapsed row = %d %s, want 503 lapsed", status, body)
	}
	store.down.Store(false)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if status, _ := call(t, server, http.MethodGet, "/readyz", nil); status == http.StatusOK {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("readyz stayed 503 after the store returned")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

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
		b := newInstance(t, store, "instance-b", testkit.EchoModel{}, true, httpapi.WithForwarding(nil, func(address string) bool {
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

// An oversized body is refused with 413 on the routes that read the raw body.
func TestOversizedBodyIsRefusedOnMessageAndNoticeRoutes(t *testing.T) {
	server, runner := newServer(t)
	s, err := runner.CreateSession(t.Context(), "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	big := `{"text":"` + strings.Repeat("x", 2<<20) + `"}`
	for _, route := range []string{"messages", "notices"} {
		resp, err := http.Post(server.URL+"/v1/sessions/"+s.ID()+"/"+route, "application/json", strings.NewReader(big))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("%s: status %d, want 413", route, resp.StatusCode)
		}
	}
}
