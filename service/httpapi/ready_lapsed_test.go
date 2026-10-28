package httpapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/httpapi"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

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
