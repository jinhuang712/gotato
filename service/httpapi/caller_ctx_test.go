package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

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

func (s *ownerStore) Create(ctx context.Context, id string, entries []session.Entry, summary session.Summary) error {
	s.note(ctx, "create")
	return s.Store.Create(ctx, id, entries, summary)
}

func (s *ownerStore) Load(ctx context.Context, id string) (session.Loaded, error) {
	caller := s.note(ctx, "load")
	loaded, err := s.Store.Load(ctx, id)
	if err == nil && loaded.Summary.Metadata["owner"] != caller {
		return session.Loaded{}, session.ErrNotFound // does not reveal that the Session exists
	}
	return loaded, err
}

// Log is how a read of a Session reaches the store, so it is guarded too.
func (s *ownerStore) Log(ctx context.Context, id string, after uint64) ([]session.Entry, error) {
	s.note(ctx, "log")
	if _, err := s.Load(ctx, id); err != nil {
		return nil, err
	}
	return s.Store.Log(ctx, id, after)
}

func (s *ownerStore) Append(ctx context.Context, id string, after uint64, entries []session.Entry, summary session.Summary) error {
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
