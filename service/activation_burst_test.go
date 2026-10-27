package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// gatedCreate holds every Create until its gate opens.
type gatedCreate struct {
	session.Store
	gate chan struct{}
}

func (s *gatedCreate) Create(ctx context.Context, id string, entries []session.Entry, summary session.RecordInfo) error {
	select {
	case <-s.gate:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.Store.Create(ctx, id, entries, summary)
}

// A burst of Runs that each create a Session cannot go past the limit while
// the first one is still writing its Session: the Runs have no ID yet, but
// each is a Session of its own.
func TestActivationLimitHoldsForABurstOfCreatingRuns(t *testing.T) {
	store := &gatedCreate{Store: session.NewMemoryStore(), gate: make(chan struct{})}
	runner, err := service.New(service.Config{
		Store:     store,
		Agents:    []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}},
		Admission: service.Admission{MaxActiveSessions: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 5)
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := runner.Run(context.Background(), service.RunRequest{Prompt: "hi"})
			errs <- err
		}()
	}
	refused := 0
	deadline := time.After(5 * time.Second)
	for refused < 4 {
		select {
		case err := <-errs:
			if !errors.Is(err, service.ErrOverloaded) {
				t.Fatalf("a Run ended with %v while the limit was held, want ErrOverloaded", err)
			}
			refused++
		case <-deadline:
			t.Fatalf("only %d of 4 Runs were refused; ActiveSessions = %d", refused, runner.ActiveSessions())
		}
	}
	if got := runner.ActiveSessions(); got != 1 {
		t.Fatalf("ActiveSessions = %d, want 1", got)
	}
	close(store.gate)
	wg.Wait()
	if err := <-errs; err != nil {
		t.Fatalf("the admitted Run: %v", err)
	}
}
