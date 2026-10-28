package service_test

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// readNumbers reads count stored Events from a stream and returns their
// numbers, failing if they do not come.
func readNumbers(t *testing.T, stream *service.SessionStream, count int) []uint64 {
	t.Helper()
	var out []uint64
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for len(out) < count {
		event, err := stream.Next(ctx)
		if err != nil {
			t.Fatalf("after %v: %v", out, err)
		}
		if event.Kind().Class() == gotato.EventClassStored {
			out = append(out, event.Head().Number)
		}
	}
	return out
}

func consecutive(from uint64, count int) []uint64 {
	out := make([]uint64, count)
	for i := range out {
		out[i] = from + uint64(i)
	}
	return out
}

// A Session's stream starts after the number a client saw: first what the
// Record holds after it, then the Events of later Runs as they happen, each
// once and in order, with the Record's epoch told up front.
func TestStreamSessionGoesOnAfterANumber(t *testing.T) {
	runner, store := newRunner(t)
	ctx := context.Background()
	first, err := runner.Run(ctx, service.RunRequest{Prompt: "one"})
	if err != nil {
		t.Fatal(err)
	}
	id := first.SessionID
	loaded, err := store.Load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	head := loaded.Head

	stream, err := runner.StreamSession(ctx, id, service.StreamFrom{After: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	if stream.RecordEpoch == "" || stream.RecordEpoch != first.RecordEpoch || stream.RecordEpoch != loaded.Info.RecordEpoch {
		t.Fatalf("stream epoch %q, the Run's %q, the store's %q", stream.RecordEpoch, first.RecordEpoch, loaded.Info.RecordEpoch)
	}
	if stream.Head != head {
		t.Fatalf("stream head = %d, want %d", stream.Head, head)
	}
	if got := readNumbers(t, stream, int(head-3)); len(got) != int(head-3) || got[0] != 4 || got[len(got)-1] != head {
		t.Fatalf("stream = %v, want 4..%d", got, head)
	}

	// A later Run arrives on the same stream, after the Events already read.
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: "two"}); err != nil {
		t.Fatal(err)
	}
	rest := readNumbers(t, stream, 5)
	if rest[0] != head+1 {
		t.Fatalf("the live part starts at %d, want %d", rest[0], head+1)
	}
	for i := 1; i < len(rest); i++ {
		if rest[i] != rest[i-1]+1 {
			t.Fatalf("live stream = %v: not consecutive", rest)
		}
	}
}

// A client whose position cannot be resumed is told so, instead of reading
// other Events under the same numbers.
func TestStreamSessionRefusesAPositionItCannotResume(t *testing.T) {
	runner, store := newRunner(t)
	ctx := context.Background()
	first, err := runner.Run(ctx, service.RunRequest{Prompt: "one"})
	if err != nil {
		t.Fatal(err)
	}
	id := first.SessionID
	loaded, _ := store.Load(ctx, id)

	if _, err := runner.StreamSession(ctx, id, service.StreamFrom{After: 2, RecordEpoch: first.RecordEpoch}); err != nil {
		t.Fatalf("the right epoch: %v", err)
	}
	// The epoch of another Record.
	_, err = runner.StreamSession(ctx, id, service.StreamFrom{After: 2, RecordEpoch: "another"})
	if !errors.Is(err, service.ErrCannotResume) || !gotato.IsCode(err, gotato.CodeCannotResume) {
		t.Fatalf("a wrong epoch: %v, want ErrCannotResume", err)
	}
	// A position the Record has not reached.
	if _, err := runner.StreamSession(ctx, id, service.StreamFrom{After: loaded.Head + 1}); !errors.Is(err, service.ErrCannotResume) {
		t.Fatalf("a position past the head: %v, want ErrCannotResume", err)
	}
	// A Session that does not exist.
	if _, err := runner.StreamSession(ctx, "missing", service.StreamFrom{}); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("a missing Session: %v, want not found", err)
	}

	// Deleted and created again under the same ID: the old numbers mean other
	// Events now, and the old epoch says so.
	if err := runner.DeleteSession(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.CreateSessionExclusive(ctx, "echo", id, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.StreamSession(ctx, id, service.StreamFrom{After: 1, RecordEpoch: first.RecordEpoch}); !errors.Is(err, service.ErrCannotResume) {
		t.Fatalf("after the Record was created again: %v, want ErrCannotResume", err)
	}
	// From the start needs no epoch.
	stream, err := runner.StreamSession(ctx, id, service.StreamFrom{})
	if err != nil {
		t.Fatal(err)
	}
	_ = stream.Close()
}

// A stream on an Instance that is not the Session's writer still follows the
// Record: the Events another Instance writes reach it.
func TestStreamSessionFollowsAnotherInstancesWrites(t *testing.T) {
	store := session.NewMemoryStore()
	build := func(name string) *service.Runner {
		runner, err := service.New(service.Config{
			Store: store, Instance: name, StreamPoll: 10 * time.Millisecond,
			Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = runner.Close(context.Background()) })
		return runner
	}
	writer, reader := build("writer"), build("reader")
	ctx := context.Background()
	created, err := writer.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := reader.StreamSession(ctx, created.ID(), service.StreamFrom{After: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	if _, err := writer.Run(ctx, service.RunRequest{SessionID: created.ID(), Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	loaded, _ := store.Load(ctx, created.ID())
	got := readNumbers(t, stream, int(loaded.Head)-1)
	if want := consecutive(2, int(loaded.Head)-1); !equalNumbers(got, want) {
		t.Fatalf("the reader saw %v, want %v", got, want)
	}
}

func equalNumbers(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// When the drain is over, a Session's stream ends after the Events already
// stored, and says to reconnect and how long to wait.
func TestDrainEndsSessionStreamsWithAReconnect(t *testing.T) {
	runner, _ := newRunner(t)
	ctx := context.Background()
	first, err := runner.Run(ctx, service.RunRequest{Prompt: "one"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := runner.StreamSession(ctx, first.SessionID, service.StreamFrom{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	if stream.Reconnect() != nil {
		t.Fatal("a stream that has not ended asks to reconnect")
	}
	readNumbers(t, stream, int(stream.Head))
	if err := runner.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		_, err := stream.Next(waitCtx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("stream ended with %v, want io.EOF", err)
		}
	}
	hint := stream.Reconnect()
	if hint == nil || hint.RetryAfterMS <= 0 || hint.Why != "draining" {
		t.Fatalf("reconnect = %+v, want a wait and the reason draining", hint)
	}
	// A stream opened after the drain ended is told at once.
	late, err := runner.StreamSession(ctx, first.SessionID, service.StreamFrom{After: stream.Head})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = late.Close() }()
	if _, err := late.Next(waitCtx); !errors.Is(err, io.EOF) || late.Reconnect() == nil {
		t.Fatalf("a stream opened after the drain: %v, %+v; want io.EOF and a reconnect", err, late.Reconnect())
	}
}

// The wait that a stream gives its client is spread out, so the clients of a
// lost instance do not all come back at the same instant.
func TestReconnectWaitsAreJittered(t *testing.T) {
	seen := map[int64]bool{}
	for range 200 {
		hint := service.NewReconnect("draining")
		if hint.RetryAfterMS < 500 || hint.RetryAfterMS >= 1500 {
			t.Fatalf("wait = %d ms, want 500 to 1499", hint.RetryAfterMS)
		}
		seen[hint.RetryAfterMS] = true
	}
	if len(seen) < 20 {
		t.Fatalf("200 waits took only %d values", len(seen))
	}
}

// replyOf is the loop FEATURES.md (G-F13) shows a chat bot: it returns the
// text of the reply that ended the next Run to settle on a Session's stream,
// that is, the last reply that asked for no tool.
func replyOf(ctx context.Context, sub gotato.Subscription) (string, error) {
	var reply string
	for {
		event, err := sub.Next(ctx)
		if err != nil {
			return "", err
		}
		switch e := event.(type) {
		case gotato.ReplyEnded:
			if e.ToolCalls == 0 {
				reply = gotato.TextOf(e.Message)
			}
		case gotato.RunEnded:
			return reply, nil
		}
	}
}

func TestSessionStreamSnippetReadsTheFinalReply(t *testing.T) {
	ctx := context.Background()
	runner, err := service.New(service.Config{
		Store:  session.NewMemoryStore(),
		Agents: []service.Agent{{Name: "echo", Model: testkit.NewFakeModel(testkit.Text("hello there"))}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close(ctx) })
	s, err := runner.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	// As in the snippet: follow the Session from its head now.
	record, err := runner.Record(ctx, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	sub, err := runner.StreamSession(ctx, s.ID(), service.StreamFrom{After: record.Head()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Close() })
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	reply, err := replyOf(ctx, sub)
	if err != nil {
		t.Fatal(err)
	}
	if reply != "hello there" {
		t.Fatalf("reply = %q, want %q", reply, "hello there")
	}
}

// loadCounter counts Load calls.
type loadCounter struct {
	session.Store
	loads atomic.Int64
}

func (c *loadCounter) Load(ctx context.Context, id string) (session.Loaded, error) {
	c.loads.Add(1)
	return c.Store.Load(ctx, id)
}

// An idle stream costs one Log read per look, and no Load: the epoch is checked
// only after a read that returned Events.
func TestIdleStreamDoesNotLoadTheSessionOnEveryLook(t *testing.T) {
	store := &loadCounter{Store: session.NewMemoryStore()}
	a := recreatedRunner(t, "a", store, 10*time.Millisecond)
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
	before := store.loads.Load()
	time.Sleep(300 * time.Millisecond) // about thirty looks
	if got := store.loads.Load() - before; got != 0 {
		t.Fatalf("an idle stream made %d Load calls in 300 ms, want none", got)
	}
}

// Instance A keeps a Session's stream open while a Run lands on B, then on A.
// The Events B wrote reach A's subscriber, read from the Record, with no gap:
// a stream never misses a stored Event, whoever wrote it.
func TestSessionStreamHasNoGapAfterAnotherInstanceWrote(t *testing.T) {
	ctx := context.Background()
	store := &watchedStore{Store: session.NewMemoryStore()}
	liveness := steadyLiveness
	a := instanceRunner(t, store, "a", testkit.EchoModel{}, liveness)
	b := instanceRunner(t, store, "b", testkit.EchoModel{}, liveness)
	s, err := a.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "p0"}); err != nil {
		t.Fatal(err)
	}
	before, err := a.Events(ctx, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	head := before[len(before)-1].Head().Number
	emptyBefore := store.emptyReads.Load()
	sub, err := a.StreamSession(ctx, s.ID(), service.StreamFrom{After: head})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()
	store.waitLive(t, emptyBefore) // the subscriber catches up and goes live
	if _, err := b.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "p1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "p2"}); err != nil {
		t.Fatal(err)
	}
	all, err := a.Events(ctx, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	last := all[len(all)-1].Head().Number
	prev := head
	read, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for prev < last {
		event, err := sub.Next(read)
		if err != nil {
			t.Fatalf("after %d: %v", prev, err)
		}
		if event.Kind().Class() != gotato.EventClassStored {
			continue
		}
		if number := event.Head().Number; number != prev+1 {
			t.Fatalf("a gap on A's Session stream: %d (%s) follows %d", number, event.Kind(), prev)
		}
		prev = event.Head().Number
	}
}

// A stream that is open while its Session is deleted and created again under
// the same ID must not go on with the new Record's Events under the old
// numbers: it ends with an error, whichever instance deleted the Session.
func TestOpenStreamEndsWhenItsSessionIsCreatedAgain(t *testing.T) {
	for _, where := range []string{"on the same instance", "on another instance"} {
		t.Run(where, func(t *testing.T) {
			store := session.NewMemoryStore()
			build := func(name string) *service.Runner {
				runner, err := service.New(service.Config{
					Store: store, Instance: name, StreamPoll: 10 * time.Millisecond,
					Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}},
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = runner.Close(context.Background()) })
				return runner
			}
			a, b := build("a"), build("b")
			other := a
			if where == "on another instance" {
				other = b
			}
			ctx := context.Background()
			first, err := a.Run(ctx, service.RunRequest{Prompt: "one"})
			if err != nil {
				t.Fatal(err)
			}
			id := first.SessionID
			stream, err := a.StreamSession(ctx, id, service.StreamFrom{})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = stream.Close() }()
			readNumbers(t, stream, int(stream.Head))

			if err := other.DeleteSession(ctx, id); err != nil {
				t.Fatal(err)
			}
			if _, err := other.CreateSessionExclusive(ctx, "echo", id, nil); err != nil {
				t.Fatal(err)
			}
			for range 3 {
				if _, err := other.Run(ctx, service.RunRequest{SessionID: id, Prompt: "new"}); err != nil {
					t.Fatal(err)
				}
			}
			read, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			for {
				event, err := stream.Next(read)
				if err != nil {
					if !errors.Is(err, service.ErrCannotResume) && !errors.Is(err, session.ErrNotFound) {
						t.Fatalf("the stream ended with %v, want cannot_resume or not found", err)
					}
					return
				}
				if event.Kind().Class() == gotato.EventClassStored {
					t.Fatalf("the stream delivered Event %d (%s) of the Record created again", event.Head().Number, event.Kind())
				}
			}
		})
	}
}

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

func (g *gatedLog) Log(ctx context.Context, id string, after uint64, limit int) ([]session.Entry, error) {
	if g.armed.CompareAndSwap(true, false) {
		g.entered <- struct{}{}
		select {
		case <-g.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return g.Store.Log(ctx, id, after, limit)
}

func recreatedRunner(t *testing.T, name string, store session.Store, poll time.Duration) *service.Runner {
	t.Helper()
	runner, err := service.New(service.Config{
		Store: store, Instance: name, StreamPoll: poll,
		Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}, ToolSources: []gotato.ToolSource{toolList{testkit.NewFakeTool("probe", "ok")}}}},
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
		if event.Kind().Class() == gotato.EventClassStored {
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
