package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// createErrorStore fails to create one ID, standing in for a Store that is
// broken rather than holding the Session already.
type createErrorStore struct {
	session.Store
	failID string
	err    error
}

func (s *createErrorStore) Create(ctx context.Context, id string, entries []session.Entry, summary session.RecordInfo) error {
	if id == s.failID {
		return s.err
	}
	return s.Store.Create(ctx, id, entries, summary)
}

func TestForkTreatsStoreErrorAsFailure(t *testing.T) {
	backing := session.NewMemoryStore()
	getErr := errors.New("store: unavailable")
	runner, err := service.New(service.Config{
		Store:  &createErrorStore{Store: backing, failID: "child", err: getErr},
		Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	parent, err := runner.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Fork(ctx, parent.ID(), "child"); !errors.Is(err, getErr) {
		t.Fatalf("fork err = %v, want the store error", err)
	}
	if _, err := session.View(ctx, backing, "child"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("fork saved a session despite a store error: %v", err)
	}
}

func TestForkWithExplicitIDIsExclusive(t *testing.T) {
	runner, _ := newRunner(t)
	ctx := context.Background()
	parent, err := runner.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	const n = 8
	var wg sync.WaitGroup
	results := make(chan error, n)
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := runner.Fork(ctx, parent.ID(), "shared-fork")
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
			continue
		}
		// A loser either finds the ID taken or is rejected while the winner
		// holds the lock; neither may overwrite the winner.
		if !errors.Is(err, service.ErrSessionExists) && !gotato.IsCode(err, gotato.CodeBusy) {
			t.Fatalf("unexpected fork err = %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful forks = %d, want exactly 1", succeeded)
	}
}

// A Fork whose ID is taken reports ErrSessionExists, as CreateSessionExclusive
// does, and leaves the Session that holds the ID as it was.
func TestForkWithATakenIDIsErrSessionExists(t *testing.T) {
	runner, _ := newRunner(t)
	ctx := context.Background()
	parent, err := runner.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Fork(ctx, parent.ID(), "taken"); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Fork(ctx, parent.ID(), "taken"); !errors.Is(err, service.ErrSessionExists) {
		t.Fatalf("second Fork = %v, want ErrSessionExists", err)
	}
	if _, err := runner.CreateSessionExclusive(ctx, "echo", "taken", nil); !errors.Is(err, service.ErrSessionExists) {
		t.Fatalf("CreateSessionExclusive = %v, want ErrSessionExists", err)
	}
}
