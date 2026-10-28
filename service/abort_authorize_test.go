package service_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

type callerKey struct{}

// refusingStore refuses the reads of the caller "mallory", as a store wrapper
// that authorizes by the request's context does, and pauses the first Load of
// anyone else.
type refusingStore struct {
	session.Store
	entered, proceed chan struct{}
	block            atomic.Bool
}

func (s *refusingStore) refused(ctx context.Context) bool { return ctx.Value(callerKey{}) == "mallory" }

func (s *refusingStore) Load(ctx context.Context, id string) (session.Loaded, error) {
	if s.refused(ctx) {
		return session.Loaded{}, session.ErrNotFound
	}
	if s.block.CompareAndSwap(true, false) {
		close(s.entered)
		select {
		case <-s.proceed:
		case <-ctx.Done():
			return session.Loaded{}, ctx.Err()
		}
	}
	return s.Store.Load(ctx, id)
}

func (s *refusingStore) Log(ctx context.Context, id string, after uint64, limit int) ([]session.Entry, error) {
	if s.refused(ctx) {
		return nil, session.ErrNotFound
	}
	return s.Store.Log(ctx, id, after, limit)
}

// AbortSession asks the store whatever stage the Run is in: a caller the store
// refuses cannot stop a Run that is still loading its Record, as it cannot
// stop one that has a live Session.
func TestAbortSessionAuthorizesWhileTheRunIsLoading(t *testing.T) {
	ctx := context.Background()
	store := &refusingStore{Store: session.NewMemoryStore(), entered: make(chan struct{}), proceed: make(chan struct{})}
	r, err := service.New(service.Config{Store: store, Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(ctx) })
	rec, err := r.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	store.block.Store(true)
	done := make(chan error, 1)
	go func() {
		_, runErr := r.Run(ctx, service.RunRequest{SessionID: rec.ID(), Prompt: "owner request"})
		done <- runErr
	}()
	<-store.entered
	abortErr := r.AbortSession(context.WithValue(ctx, callerKey{}, "mallory"), rec.ID())
	close(store.proceed)
	if runErr := <-done; runErr != nil {
		t.Fatalf("the owner's Run was stopped by a refused caller: %v", runErr)
	}
	if !errors.Is(abortErr, session.ErrNotFound) {
		t.Fatalf("AbortSession by a refused caller = %v, want not_found", abortErr)
	}
}
