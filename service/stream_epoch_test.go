package service_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// readUntilEnd reads a stream on its own goroutine, as an HTTP handler does,
// and reports the numbers of the stored Events it delivered and the error it
// ended with.
func readUntilEnd(stream gotato.Subscription) <-chan streamEnd {
	ended := make(chan streamEnd, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var end streamEnd
		for {
			event, err := stream.Next(ctx)
			if err != nil {
				end.err = err
				ended <- end
				return
			}
			if event.Kind().Class() == gotato.ClassStored {
				end.numbers = append(end.numbers, event.Head().Number)
			}
		}
	}()
	return ended
}

type streamEnd struct {
	numbers []uint64
	err     error
}

// Opening a Record created again for a tool toggle or a compaction writes a
// claim numbered one past an old stream's cursor when the two Records have
// the same length. The claim never reaches the old stream, which ends.
func TestOpeningARecordCreatedAgainSendsNoClaimToAnOldStream(t *testing.T) {
	ctx := context.Background()
	changes := []struct {
		name   string
		change func(runner *service.Runner, id string)
	}{
		{"a tool toggle", func(runner *service.Runner, id string) {
			// An unknown tool: the Record is opened and claimed, then refused.
			_, _ = runner.SetToolActive(ctx, id, "no.such.tool", false)
		}},
		{"a compaction", func(runner *service.Runner, id string) {
			_, _ = runner.Compact(ctx, id, modelctx.CompactOptions{Keep: 2})
		}},
	}
	for _, c := range changes {
		t.Run(c.name, func(t *testing.T) {
			// Each try leaked the claim about half the time before the fix.
			for try := range 20 {
				store := session.NewMemoryStore()
				a, b := recreatedRunner(t, "a", store, -1), recreatedRunner(t, "b", store, -1)
				id := fmt.Sprintf("session-%d", try)
				shape := func(runner *service.Runner) {
					if _, err := runner.CreateSessionExclusive(ctx, "echo", id, nil); err != nil {
						t.Fatal(err)
					}
					if _, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: "one"}); err != nil {
						t.Fatal(err)
					}
				}
				shape(a)
				stream, err := a.StreamSession(ctx, id, service.StreamFrom{})
				if err != nil {
					t.Fatal(err)
				}
				readNumbers(t, stream, int(stream.Head))
				// The catch-up's last, empty read, before the delete.
				time.Sleep(20 * time.Millisecond)
				ended := readUntilEnd(stream)
				if err := b.DeleteSession(ctx, id); err != nil {
					t.Fatal(err)
				}
				shape(b)
				if again, err := store.Load(ctx, id); err != nil || again.Head != stream.Head {
					t.Fatalf("the new Record ends at %d (%v), want the old one's %d", again.Head, err, stream.Head)
				}
				c.change(a, id)
				select {
				case end := <-ended:
					if len(end.numbers) > 0 {
						t.Fatalf("try %d: the old stream delivered Events %v of the Record created again", try, end.numbers)
					}
					if !errors.Is(end.err, service.ErrCannotResume) {
						t.Fatalf("try %d: the old stream ended with %v, want cannot_resume", try, end.err)
					}
				case <-time.After(5 * time.Second):
					t.Fatalf("try %d: the old stream never ended", try)
				}
				_ = stream.Close()
			}
		})
	}
}

// waitEnd waits for a stream read by readUntilEnd to end, and fails the test
// if it delivered a stored Event or ended with anything but cannot_resume or
// not found.
func waitEnd(t *testing.T, ended <-chan streamEnd) {
	t.Helper()
	select {
	case end := <-ended:
		if len(end.numbers) > 0 {
			t.Fatalf("the old stream delivered Events %v of the Record created again", end.numbers)
		}
		if !errors.Is(end.err, service.ErrCannotResume) && !errors.Is(end.err, session.ErrNotFound) {
			t.Fatalf("the old stream ended with %v, want cannot_resume or not found", end.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the old stream never ended")
	}
}

// A stream of a Record created again is not held back by a stream of the old
// Record on the same instance: each Record has a Feed of its own, so the new
// stream's catch-up rejoins the live Events at the new Record's last number,
// not at the old one's. Opening it also ends the old stream, which this
// instance now knows to be of a Record that was created again.
func TestNewStreamIsNotHeldBackByAStreamOfTheOldRecord(t *testing.T) {
	store := session.NewMemoryStore()
	// No looks: one that fell between the delete and the create would end the
	// old stream with not found, by chance.
	a, b := recreatedRunner(t, "a", store, -1), recreatedRunner(t, "b", store, -1)
	ctx := context.Background()
	first, err := a.Run(ctx, service.RunRequest{Prompt: "one"})
	if err != nil {
		t.Fatal(err)
	}
	id := first.SessionID
	old, err := a.StreamSession(ctx, id, service.StreamFrom{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = old.Close() }()
	// The old stream follows a Run live, so this instance's Feed has seen the
	// old Record's last number.
	if _, err := a.Run(ctx, service.RunRequest{SessionID: id, Prompt: "two"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	oldHead := loaded.Head
	readNumbers(t, old, int(oldHead))
	// Give the old stream's catch-up its last, empty read, so it is live
	// before the delete: a read during the delete would end it with not found.
	time.Sleep(50 * time.Millisecond)
	oldEnded := readUntilEnd(old)

	if err := b.DeleteSession(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CreateSessionExclusive(ctx, "echo", id, nil); err != nil {
		t.Fatal(err)
	}
	fresh, err := a.StreamSession(ctx, id, service.StreamFrom{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fresh.Close() }()
	if fresh.Head >= oldHead {
		t.Fatalf("the new Record ends at %d, want before the old one's %d", fresh.Head, oldHead)
	}
	readNumbers(t, fresh, int(fresh.Head))
	wait, cancel := context.WithTimeout(ctx, time.Second)
	event, err := fresh.Next(wait)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the stream of the new Record, idle at its last Event, gave %v, %v; want it to wait", event, err)
	}

	// A Run of the new Record on this instance reaches the new stream live.
	if _, err := a.Run(ctx, service.RunRequest{SessionID: id, Prompt: "new"}); err != nil {
		t.Fatal(err)
	}
	record, err := a.Session(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	got := readNumbers(t, fresh, int(record.Head()-fresh.Head))
	if want := consecutive(fresh.Head+1, int(record.Head()-fresh.Head)); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("the new stream delivered %v, want %v", got, want)
	}
	waitEnd(t, oldEnded)
}

// loadGate holds the next Load, once armed, after the store has answered it,
// so a caller works with what it read while the store changes.
type loadGate struct {
	session.Store
	armed   atomic.Bool
	loaded  chan struct{}
	release chan struct{}
}

func (g *loadGate) Load(ctx context.Context, id string) (session.Loaded, error) {
	loaded, err := g.Store.Load(ctx, id)
	if g.armed.CompareAndSwap(true, false) {
		g.loaded <- struct{}{}
		select {
		case <-g.release:
		case <-ctx.Done():
			return session.Loaded{}, ctx.Err()
		}
	}
	return loaded, err
}

// holdOnce holds its next reply, once armed, until its gate opens.
type holdOnce struct {
	inner   gotato.Model
	armed   atomic.Bool
	started chan struct{}
	gate    chan struct{}
}

func (m *holdOnce) Stream(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	if m.armed.CompareAndSwap(true, false) {
		close(m.started)
		select {
		case <-m.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return m.inner.Stream(ctx, request)
}

// A stream that read the old Record's epoch, and opens while the Session is
// deleted and created again and a Run on this instance opens the new Record,
// does not follow the new Record: it is refused, or it ends, and it delivers
// none of the Run's Events as if they continued the old Record.
func TestStreamThatReadTheOldRecordDoesNotFollowTheNewOne(t *testing.T) {
	store := session.NewMemoryStore()
	gate := &loadGate{Store: store, loaded: make(chan struct{}, 1), release: make(chan struct{})}
	model := &holdOnce{inner: testkit.DemoModel{}, started: make(chan struct{}), gate: make(chan struct{})}
	a, err := service.New(service.Config{
		Store: gate, Instance: "a", StreamPoll: -1,
		Agents: []service.Agent{{Name: "echo", Model: model, Tools: []gotato.Tool{testkit.DemoEchoTool()}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close(context.Background()) })
	b := recreatedRunner(t, "b", store, -1)
	ctx := context.Background()
	first, err := b.Run(ctx, service.RunRequest{Prompt: "one"})
	if err != nil {
		t.Fatal(err)
	}
	id := first.SessionID
	before, err := store.Load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	// The stream reads the old Record, and is held before it starts.
	type opened struct {
		stream *service.SessionStream
		err    error
	}
	result := make(chan opened, 1)
	gate.armed.Store(true)
	go func() {
		stream, err := a.StreamSession(ctx, id, service.StreamFrom{After: before.Head})
		result <- opened{stream, err}
	}()
	select {
	case <-gate.loaded:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream never read the Record")
	}

	// Meanwhile the Session is deleted and created again, and a Run on this
	// instance opens the new Record and is held at its model call.
	if err := b.DeleteSession(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CreateSessionExclusive(ctx, "echo", id, nil); err != nil {
		t.Fatal(err)
	}
	model.armed.Store(true)
	ran := make(chan error, 1)
	go func() {
		_, err := a.Run(ctx, service.RunRequest{SessionID: id, Prompt: "use-tool"})
		ran <- err
	}()
	select {
	case <-model.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the Run never called its model")
	}
	held, err := store.Load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if held.Head > before.Head {
		t.Fatalf("the new Record is at %d while the Run is held, want at most %d", held.Head, before.Head)
	}
	close(gate.release)
	got := <-result
	close(model.gate)
	if err := <-ran; err != nil {
		t.Fatal(err)
	}
	after, err := store.Load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Head <= before.Head {
		t.Fatalf("the new Record ends at %d, want past %d", after.Head, before.Head)
	}
	if got.err != nil {
		if !errors.Is(got.err, service.ErrCannotResume) {
			t.Fatalf("the stream was refused with %v, want cannot_resume", got.err)
		}
		return
	}
	defer func() { _ = got.stream.Close() }()
	endsWithoutNewEvents(t, got.stream)
}
