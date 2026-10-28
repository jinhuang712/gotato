package service_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
)

// newRunnerOn is newRunner on the given store, with a short liveness, closed
// with the test.
func newRunnerOn(t *testing.T, store session.Store, agent service.Agent, mutate ...func(*service.Config)) *service.Runner {
	t.Helper()
	cfg := service.Config{Store: store, Agents: []service.Agent{agent}, Liveness: service.Liveness{TTL: time.Second, Wait: 30 * time.Millisecond}}
	for _, m := range mutate {
		m(&cfg)
	}
	r, err := service.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(context.Background()) })
	return r
}

func waitChan(t *testing.T, what string, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// kindGate blocks, once per kind, the first Append that carries an Entry of
// that kind, until the test lets it go.
type kindGate struct {
	session.Store
	mu    sync.Mutex
	holds map[gotato.EventKind]*hold
}

type hold struct{ entered, proceed chan struct{} }

func newKindGate(store session.Store, kinds ...gotato.EventKind) *kindGate {
	g := &kindGate{Store: store, holds: map[gotato.EventKind]*hold{}}
	for _, k := range kinds {
		g.holds[k] = &hold{entered: make(chan struct{}), proceed: make(chan struct{})}
	}
	return g
}

func (s *kindGate) Append(ctx context.Context, id string, after uint64, entries []session.Entry, info session.RecordInfo) error {
	var hit *hold
	s.mu.Lock()
	for _, e := range entries {
		if h := s.holds[e.Kind]; h != nil {
			hit = h
			delete(s.holds, e.Kind)
			break
		}
	}
	s.mu.Unlock()
	if hit != nil {
		close(hit.entered)
		<-hit.proceed
	}
	return s.Store.Append(ctx, id, after, entries, info)
}
