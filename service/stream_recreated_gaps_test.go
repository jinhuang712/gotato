package service_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// gatedLog holds the next Log call, once armed, until it is released, and says
// when the call has started.
type gatedLog struct {
	session.Store
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func newGatedLog(store session.Store) *gatedLog {
	return &gatedLog{Store: store, entered: make(chan struct{}, 4), release: make(chan struct{})}
}

func (g *gatedLog) Log(ctx context.Context, id string, after uint64) ([]session.Entry, error) {
	if g.armed.CompareAndSwap(true, false) {
		g.entered <- struct{}{}
		select {
		case <-g.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return g.Store.Log(ctx, id, after)
}

func recreatedRunner(t *testing.T, name string, store session.Store, poll time.Duration) *service.Runner {
	t.Helper()
	runner, err := service.New(service.Config{
		Store: store, Instance: name, StreamPoll: poll,
		Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close(context.Background()) })
	return runner
}

// recreate deletes a Session and creates it again under its ID, and runs it
// three times, on runner.
func recreate(t *testing.T, runner *service.Runner, id string) {
	t.Helper()
	ctx := context.Background()
	if err := runner.DeleteSession(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.CreateSessionExclusive(ctx, "echo", id, nil); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: "new"}); err != nil {
			t.Fatal(err)
		}
	}
}

// endsWithoutNewEvents reads sub until it fails, and fails the test if a
// stored Event comes first or the error is not cannot_resume or not found.
func endsWithoutNewEvents(t *testing.T, sub gotato.Subscription) {
	t.Helper()
	read, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		event, err := sub.Next(read)
		if err != nil {
			if !errors.Is(err, service.ErrCannotResume) && !errors.Is(err, session.ErrNotFound) {
				t.Fatalf("the stream ended with %v, want cannot_resume or not found", err)
			}
			return
		}
		if event.Kind().Class() == gotato.ClassStored {
			t.Fatalf("the stream delivered Event %d (%s) of the Record created again", event.Head().Number, event.Kind())
		}
	}
}

// (a) The Session is deleted and created again on another instance, and the
// Runs come to the instance that holds the stream, before any look in the
// store: opening the new Record ends the stream.
func TestStreamEndsWhenThisInstanceOpensARecordCreatedAgainElsewhere(t *testing.T) {
	store := session.NewMemoryStore()
	a, b := recreatedRunner(t, "a", store, -1), recreatedRunner(t, "b", store, -1)
	ctx := context.Background()
	first, err := a.Run(ctx, service.RunRequest{Prompt: "one"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := a.StreamSession(ctx, first.SessionID, service.StreamFrom{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	readNumbers(t, stream, int(stream.Head))
	if err := b.DeleteSession(ctx, first.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CreateSessionExclusive(ctx, "echo", first.SessionID, nil); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := a.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "new"}); err != nil {
			t.Fatal(err)
		}
	}
	endsWithoutNewEvents(t, stream)
}

// (b) The Record is created again while the stream's look is under way: what
// the look read is checked after the read, so none of it is delivered.
func TestStreamChecksTheEpochAfterTheReadNotBefore(t *testing.T) {
	store := session.NewMemoryStore()
	gate := newGatedLog(store)
	a, b := recreatedRunner(t, "a", gate, 10*time.Millisecond), recreatedRunner(t, "b", store, -1)
	ctx := context.Background()
	first, err := a.Run(ctx, service.RunRequest{Prompt: "one"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := a.StreamSession(ctx, first.SessionID, service.StreamFrom{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	readNumbers(t, stream, int(stream.Head))
	gate.armed.Store(true)
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream never looked in the store")
	}
	recreate(t, b, first.SessionID)
	close(gate.release)
	endsWithoutNewEvents(t, stream)
}

// (c) The Session is deleted and created again on another instance while the
// stream's first catch-up is reading: what the catch-up read is checked after
// the read, as a look's is, so none of it is delivered. (A cut on this
// instance is TestACutStreamEndsAtOnce.)
func TestFirstCatchUpChecksTheEpochAfterItsRead(t *testing.T) {
	store := session.NewMemoryStore()
	gate := newGatedLog(store)
	a, b := recreatedRunner(t, "a", gate, -1), recreatedRunner(t, "b", store, -1)
	ctx := context.Background()
	first, err := a.Run(ctx, service.RunRequest{Prompt: "one"})
	if err != nil {
		t.Fatal(err)
	}
	gate.armed.Store(true)
	stream, err := a.StreamSession(ctx, first.SessionID, service.StreamFrom{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the catch-up never read the store")
	}
	if err := b.DeleteSession(ctx, first.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CreateSessionExclusive(ctx, "echo", first.SessionID, nil); err != nil {
		t.Fatal(err)
	}
	close(gate.release)
	endsWithoutNewEvents(t, stream)
}

// (d) The Go API's StreamSession ends the same way.
func TestStreamSessionEndsWhenTheSessionIsCreatedAgain(t *testing.T) {
	store := session.NewMemoryStore()
	a := recreatedRunner(t, "a", store, -1)
	ctx := context.Background()
	first, err := a.Run(ctx, service.RunRequest{Prompt: "one"})
	if err != nil {
		t.Fatal(err)
	}
	record, err := a.Record(ctx, first.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := a.StreamSession(ctx, first.SessionID, service.StreamFrom{After: record.Head()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()
	recreate(t, a, first.SessionID)
	endsWithoutNewEvents(t, sub)
}
