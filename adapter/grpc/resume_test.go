package grpcadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato/gotato-core"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	gotatov1 "github.com/jinhuang712/gotato/adapter/grpc/gotato/v1"
)

func echoRunner(t *testing.T) *service.Runner {
	t.Helper()
	runner, err := service.New(service.Config{Store: session.NewMemoryStore(), Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}}})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

// numberOf returns an Event's number when it is a stored Event.
func storedNumber(event *gotatov1.Event) (uint64, bool) {
	if gotato.EventKind(KindOf(event)).Class() != gotato.ClassStored {
		return 0, false
	}
	return event.GetHeader().GetNumber(), true
}

// readSession reads a stream until count stored Events came.
func readSession(t *testing.T, stream gotatov1.SessionService_StreamSessionClient, count int) []*gotatov1.Event {
	t.Helper()
	var out []*gotatov1.Event
	for len(out) < count {
		update, err := stream.Recv()
		if err != nil {
			t.Fatalf("after %d of %d stored Events: %v", len(out), count, err)
		}
		if event := update.GetEvent(); event != nil {
			if _, stored := storedNumber(event); stored {
				out = append(out, event)
			}
		}
	}
	return out
}

// StreamSession starts after the number a client saw, tells the Record's epoch
// in its header metadata and its first message, and goes on live across Runs.
func TestStreamSessionResumesAfterANumber(t *testing.T) {
	runner := echoRunner(t)
	client := clientFor(t, runner)
	ctx := context.Background()
	first, err := client.Run(ctx, &gotatov1.RunRequest{Input: &gotatov1.RunRequest_Prompt{Prompt: "one"}})
	if err != nil {
		t.Fatal(err)
	}
	if first.GetRecordEpoch() == "" {
		t.Fatal("the result carries no record_epoch")
	}
	id := first.GetSessionId()
	record, err := runner.Session(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	head := int(record.Head())

	stream, err := client.StreamSession(ctx, &gotatov1.StreamSessionRequest{SessionId: id, After: 3, RecordEpoch: first.GetRecordEpoch()})
	if err != nil {
		t.Fatal(err)
	}
	update, err := stream.Recv()
	start := update.GetStart()
	if err != nil || start == nil || start.GetRecordEpoch() != first.GetRecordEpoch() || start.GetAfter() != 3 || start.GetHead() != uint64(head) || start.GetSessionId() != id {
		t.Fatalf("first message = %v, %v; want the start with epoch %q, after 3, head %d", update, err, first.GetRecordEpoch(), head)
	}
	header, err := stream.Header()
	if err != nil || len(header.Get(RecordEpochKey)) != 1 || header.Get(RecordEpochKey)[0] != first.GetRecordEpoch() {
		t.Fatalf("header = %v, %v", header, err)
	}
	events := readSession(t, stream, head-3)
	for i, event := range events {
		if number, _ := storedNumber(event); number != uint64(4+i) {
			t.Fatalf("Event %d has number %d, want %d", i, number, 4+i)
		}
	}
	go func() { _, _ = runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: "two"}) }()
	live := readSession(t, stream, 3)
	if number, _ := storedNumber(live[0]); number != uint64(head)+1 {
		t.Fatalf("the live part starts at %d, want %d", number, head+1)
	}
}

// A position that cannot be resumed fails before any message, with the
// Record's epoch in the trailer.
func TestStreamSessionRefusesWhatItCannotResume(t *testing.T) {
	runner := echoRunner(t)
	client := clientFor(t, runner)
	ctx := context.Background()
	first, err := client.Run(ctx, &gotatov1.RunRequest{Input: &gotatov1.RunRequest_Prompt{Prompt: "one"}})
	if err != nil {
		t.Fatal(err)
	}
	id := first.GetSessionId()
	record, _ := runner.Session(ctx, id)

	recv := func(request *gotatov1.StreamSessionRequest) ([]string, error) {
		stream, err := client.StreamSession(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		_, err = stream.Recv()
		return stream.Trailer().Get(RecordEpochKey), err
	}
	trailer, err := recv(&gotatov1.StreamSessionRequest{SessionId: id, After: 2, RecordEpoch: "other"})
	if status.Code(err) != codes.FailedPrecondition || len(trailer) != 1 || trailer[0] != first.GetRecordEpoch() {
		t.Fatalf("another epoch: %v, trailer %v; want FAILED_PRECONDITION naming %q", err, trailer, first.GetRecordEpoch())
	}
	if _, err := recv(&gotatov1.StreamSessionRequest{SessionId: id, After: record.Head() + 1}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("past the head: %v", err)
	}
	if _, err := recv(&gotatov1.StreamSessionRequest{SessionId: "missing"}); status.Code(err) != codes.NotFound {
		t.Fatalf("a missing Session: %v", err)
	}
	if err := withSessionIDErr(t, client, id); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("session-id metadata for another Session: %v, want INVALID_ARGUMENT", err)
	}

	// Created again under the same ID.
	if _, err := client.DeleteSession(ctx, &gotatov1.SessionRequest{SessionId: id}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateSession(ctx, &gotatov1.CreateSessionRequest{Id: id}); err != nil {
		t.Fatal(err)
	}
	if _, err := recv(&gotatov1.StreamSessionRequest{SessionId: id, After: 1, RecordEpoch: first.GetRecordEpoch()}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("a Record created again: %v", err)
	}
}

func withSessionIDErr(t *testing.T, client gotatov1.SessionServiceClient, id string) error {
	t.Helper()
	stream, err := client.StreamSession(withSessionID(context.Background(), "other"), &gotatov1.StreamSessionRequest{SessionId: id})
	if err != nil {
		t.Fatal(err)
	}
	_, err = stream.Recv()
	return err
}

// When the drain is over, the stream ends after the stored Events with
// UNAVAILABLE and a retry-after-ms trailer.
func TestDrainEndsASessionStreamWithUnavailable(t *testing.T) {
	runner := echoRunner(t)
	client := clientFor(t, runner)
	ctx := context.Background()
	first, err := client.Run(ctx, &gotatov1.RunRequest{Input: &gotatov1.RunRequest_Prompt{Prompt: "one"}})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.StreamSession(ctx, &gotatov1.StreamSessionRequest{SessionId: first.GetSessionId()})
	if err != nil {
		t.Fatal(err)
	}
	update, err := stream.Recv()
	if err != nil || update.GetStart() == nil {
		t.Fatalf("start = %v, %v", update, err)
	}
	readSession(t, stream, int(update.GetStart().GetHead()))
	if err := runner.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = stream.Recv()
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("the stream ended with %v, want UNAVAILABLE", err)
	}
	values := stream.Trailer().Get("retry-after-ms")
	if len(values) != 1 {
		t.Fatalf("trailer = %v", stream.Trailer())
	}
	if ms, err := strconv.ParseInt(values[0], 10, 64); err != nil || ms < 500 || ms >= 1500 {
		t.Fatalf("retry-after-ms = %q, want 500 to 1499", values[0])
	}
}

// ---- the exit test ----------------------------------------------------------

// normalized writes an Event as JSON with every time as a UTC instant, since a
// live Event and one read from the Record write them in different zones.
func normalized(event *gotatov1.Event) string {
	raw, _ := protojson.Marshal(event)
	var tree any
	_ = json.Unmarshal(raw, &tree)
	out, _ := json.Marshal(utc(tree))
	return string(out)
}

func utc(v any) any {
	switch value := v.(type) {
	case map[string]any:
		for k, x := range value {
			value[k] = utc(x)
		}
	case []any:
		for i, x := range value {
			value[i] = utc(x)
		}
	case string:
		if at, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return at.UTC().Format(time.RFC3339Nano)
		}
	}
	return v
}

type follower struct {
	mu     sync.Mutex
	stored []*gotatov1.Event
	deltas []*gotatov1.Event
	done   chan struct{}
}

func follow(stream gotatov1.SessionService_StreamSessionClient) *follower {
	f := &follower{done: make(chan struct{})}
	go func() {
		defer close(f.done)
		for {
			update, err := stream.Recv()
			if err != nil {
				return
			}
			event := update.GetEvent()
			if event == nil {
				continue
			}
			f.mu.Lock()
			if _, stored := storedNumber(event); stored {
				f.stored = append(f.stored, event)
			} else {
				f.deltas = append(f.deltas, event)
			}
			f.mu.Unlock()
		}
	}()
	return f
}

func (f *follower) reached(t *testing.T, n uint64) []*gotatov1.Event {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		if len(f.stored) > 0 {
			if number, _ := storedNumber(f.stored[len(f.stored)-1]); number >= n {
				out := append([]*gotatov1.Event(nil), f.stored...)
				f.mu.Unlock()
				return out
			}
		}
		f.mu.Unlock()
		select {
		case <-f.done:
			t.Fatalf("the stream ended before Event %d", n)
		case <-time.After(2 * time.Millisecond):
		}
	}
	t.Fatalf("the stream never reached Event %d", n)
	return nil
}

func cutAndResume(t *testing.T, task testkit.Task, k int) (uncut, resumed []*gotatov1.Event) {
	runner, err := service.New(service.Config{
		Store: session.NewMemoryStore(),
		Agents: []service.Agent{{
			Name: task.Name, Model: task.Model, Instruction: task.Instruction, Tools: task.Tools, Options: task.Options, Limits: task.Limits,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	client := clientFor(t, runner)
	ctx := context.Background()
	created, err := runner.CreateSession(ctx, task.Name, nil, session.WithMessages(task.History...))
	if err != nil {
		t.Fatal(err)
	}
	id := created.ID()

	wholeCtx, stopWhole := context.WithCancel(ctx)
	defer stopWhole()
	ws, err := client.StreamSession(wholeCtx, &gotatov1.StreamSessionRequest{SessionId: id})
	if err != nil {
		t.Fatal(err)
	}
	whole := follow(ws)

	cutCtx, cut := context.WithCancel(ctx)
	defer cut()
	cs, err := client.StreamSession(cutCtx, &gotatov1.StreamSessionRequest{SessionId: id})
	if err != nil {
		t.Fatal(err)
	}
	update, err := cs.Recv()
	if err != nil || update.GetStart() == nil {
		t.Fatalf("start = %v, %v", update, err)
	}
	epoch := update.GetStart().GetRecordEpoch()

	final := make(chan uint64, 1)
	go func() {
		for _, prompt := range task.Prompts {
			if _, err := client.Run(ctx, &gotatov1.RunRequest{SessionId: id, Input: &gotatov1.RunRequest_Prompt{Prompt: prompt}}); err != nil {
				t.Error(err)
				final <- 0
				return
			}
		}
		record, err := runner.Session(ctx, id)
		if err != nil {
			t.Error(err)
			final <- 0
			return
		}
		final <- record.Head()
	}()

	firstPart := readSession(t, cs, k)
	cut() // the connection is cut here
	rs, err := client.StreamSession(ctx, &gotatov1.StreamSessionRequest{SessionId: id, After: uint64(k), RecordEpoch: epoch})
	if err != nil {
		t.Fatal(err)
	}
	again := follow(rs)

	last := <-final
	if last == 0 {
		t.FailNow()
	}
	uncut = whole.reached(t, last)
	var secondPart []*gotatov1.Event
	if uint64(k) < last {
		secondPart = again.reached(t, last)
	}
	resumed = append(append([]*gotatov1.Event(nil), firstPart...), secondPart...)

	again.mu.Lock()
	for _, d := range again.deltas {
		if d.GetHeader().GetNumber() < uint64(k) {
			t.Errorf("the resumed stream sent a delta that follows Event %d, before the cut at %d", d.GetHeader().GetNumber(), k)
		}
	}
	again.mu.Unlock()
	return uncut, resumed
}

// TestStreamCutAtEveryEventResumesEqualToAnUncutOne is the Phase 5 exit test,
// over gRPC: for each Event of each reference task, a Session's stream is cut
// after that Event and resumed after its number, and what the client read is
// the same as what a stream that was never cut read, with no gap and no repeat.
func TestStreamCutAtEveryEventResumesEqualToAnUncutOne(t *testing.T) {
	for _, task := range testkit.ReferenceTasks() {
		t.Run(task.Name, func(t *testing.T) {
			calibration, _ := cutAndResume(t, task, 1)
			total := len(calibration)
			if total < 6 {
				t.Fatalf("a play made only %d Events", total)
			}
			t.Logf("%d Events, a cut after each", total)
			for k := 1; k <= total; k++ {
				uncut, resumed := cutAndResume(t, task, k)
				for i, event := range uncut {
					if number, _ := storedNumber(event); number != uint64(i+1) {
						t.Fatalf("cut after %d: the uncut stream has Event %d at place %d", k, number, i+1)
					}
				}
				got, want := make([]string, len(resumed)), make([]string, len(uncut))
				for i, e := range resumed {
					got[i] = normalized(e)
				}
				for i, e := range uncut {
					want[i] = normalized(e)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("cut after %d: the resumed stream differs from the uncut one (%d Events, uncut %d); %s", k, len(got), len(want), firstDifference(got, want))
				}
			}
		})
	}
}

func firstDifference(got, want []string) string {
	for i := range min(len(got), len(want)) {
		if got[i] != want[i] {
			return fmt.Sprintf("at place %d:\n got  %s\n want %s", i+1, got[i], want[i])
		}
	}
	return "one is a prefix of the other"
}
