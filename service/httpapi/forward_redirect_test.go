package httpapi_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/httpapi"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

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
