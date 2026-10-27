package service_test

import (
	"context"
	"errors"
	"io"
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
		if event.Kind().Class() == gotato.ClassStored {
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
	if stream.RecordEpoch == "" || stream.RecordEpoch != first.RecordEpoch || stream.RecordEpoch != loaded.Info.Epoch() {
		t.Fatalf("stream epoch %q, the Run's %q, the store's %q", stream.RecordEpoch, first.RecordEpoch, loaded.Info.Epoch())
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
