package service_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
)

// holdStore holds one call of a store, once armed, and says when it is held.
// A held Append has written already: it is held on its way back to the caller.
// A held Load has read already, too. Each kind of call has its own release, so
// that a call is let go by the test that means it, whichever of the two reaches
// its wait first: a goroutine that has said it is held may be slow to wait.
type holdStore struct {
	session.Store
	armAppend, armLoad         atomic.Bool
	held                       chan struct{}
	releaseAppend, releaseLoad chan struct{}
}

func newHoldStore(store session.Store) *holdStore {
	return &holdStore{Store: store, held: make(chan struct{}, 4), releaseAppend: make(chan struct{}), releaseLoad: make(chan struct{})}
}

func (h *holdStore) hold(release <-chan struct{}) {
	h.held <- struct{}{}
	<-release
}

func (h *holdStore) Append(ctx context.Context, id string, after uint64, entries []session.Entry, summary session.Summary) error {
	err := h.Store.Append(ctx, id, after, entries, summary)
	if err == nil && h.armAppend.CompareAndSwap(true, false) {
		h.hold(h.releaseAppend)
	}
	return err
}

func (h *holdStore) Load(ctx context.Context, id string) (session.Loaded, error) {
	loaded, err := h.Store.Load(ctx, id)
	if err == nil && h.armLoad.CompareAndSwap(true, false) {
		h.hold(h.releaseLoad)
	}
	return loaded, err
}

// cutRaceRunners builds the instance "a", whose store calls the test can hold,
// and the instance "b" on the same store, and runs a Session once on a.
func cutRaceRunners(t *testing.T) (a, b *service.Runner, held *holdStore, id string) {
	t.Helper()
	held = newHoldStore(session.NewMemoryStore())
	a = recreatedRunner(t, "a", held, 10*time.Millisecond)
	b = recreatedRunner(t, "b", held, 10*time.Millisecond)
	first, err := a.Run(context.Background(), service.RunRequest{Prompt: "one"})
	if err != nil {
		t.Fatal(err)
	}
	return a, b, held, first.SessionID
}

// notCut fails the test when the stream was cut. Next returns the cut error
// first if there is one; with a canceled context it returns an Event or the
// context's error otherwise.
func notCut(t *testing.T, stream *service.SessionStream) {
	t.Helper()
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := stream.Next(gone); errors.Is(err, service.ErrCannotResume) || errors.Is(err, session.ErrNotFound) {
		t.Fatalf("the stream of the Record created again was cut with %v", err)
	}
}

// cut fails the test unless the stream was cut with cannot_resume.
func cut(t *testing.T, stream *service.SessionStream) {
	t.Helper()
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := stream.Next(gone); !errors.Is(err, service.ErrCannotResume) {
		t.Fatalf("the stream of the Record replaced was not cut, Next returned %v", err)
	}
}

// An open writes its claim, and only then cuts the streams of other Records. If
// the Record is deleted and created again on another instance in between, and a
// client streams the new Record on this instance, that stream is of the newest
// Record, and the open must not cut it.
func TestOpenDoesNotCutAStreamOfANewerRecord(t *testing.T) {
	a, b, held, id := cutRaceRunners(t)
	ctx := context.Background()

	held.armAppend.Store(true)
	opened := make(chan error, 1)
	go func() {
		_, err := a.Run(ctx, service.RunRequest{SessionID: id, Prompt: "two"})
		opened <- err
	}()
	<-held.held // a's claim is written, and a has not cut any stream yet

	if err := b.DeleteSession(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CreateSessionExclusive(ctx, "echo", id, nil); err != nil {
		t.Fatal(err)
	}
	stream, err := a.StreamSession(ctx, id, service.StreamFrom{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()

	close(held.releaseAppend)
	<-opened // a's Run ended; its error is not what is tested
	notCut(t, stream)
}

// A stream that read the Record before the open did, but only started after it,
// may be of an older Record: the open reads the Record again to tell, and cuts
// it when the Record has another epoch.
func TestOpenCutsAStreamOfAnOlderRecordThatStartsLate(t *testing.T) {
	a, b, held, id := cutRaceRunners(t)
	ctx := context.Background()

	// The stream reads the Record, and is held before it starts.
	held.armLoad.Store(true)
	type result struct {
		stream *service.SessionStream
		err    error
	}
	started := make(chan result, 1)
	go func() {
		stream, err := a.StreamSession(ctx, id, service.StreamFrom{})
		started <- result{stream, err}
	}()
	<-held.held

	if err := b.DeleteSession(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CreateSessionExclusive(ctx, "echo", id, nil); err != nil {
		t.Fatal(err)
	}

	// An open of the new Record on a writes its claim, and is held.
	held.armAppend.Store(true)
	opened := make(chan error, 1)
	go func() {
		_, err := a.Run(ctx, service.RunRequest{SessionID: id, Prompt: "two"})
		opened <- err
	}()
	<-held.held

	// The stream goes on and starts, with the epoch it read, before the open cuts.
	held.releaseLoad <- struct{}{}
	got := <-started
	if got.err != nil {
		t.Fatal(got.err)
	}
	defer func() { _ = got.stream.Close() }()

	held.releaseAppend <- struct{}{}
	<-opened
	cut(t, got.stream)
}
