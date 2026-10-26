package grpcadapter

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato/gotato-core"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	gotatov2 "github.com/jinhuang712/gotato/adapter/grpc/gotato/v2"
)

// chattyModel streams far more text than a gRPC stream's flow-control window
// holds, in many small deltas.
type chattyModel struct{ deltas int }

func (m chattyModel) Stream(context.Context, gotato.ModelRequest) (gotato.ModelStream, error) {
	return &chattyStream{left: m.deltas}, nil
}

type chattyStream struct {
	left int
	done bool
}

func (s *chattyStream) Recv(context.Context) (gotato.ModelEvent, error) {
	switch {
	case s.left > 0:
		s.left--
		return gotato.ModelEvent{Kind: gotato.ModelTextDelta, Text: strings.Repeat("x", 1024)}, nil
	case !s.done:
		s.done = true
		return gotato.ModelEvent{Kind: gotato.ModelDone, StopReason: gotato.StopEndTurn}, nil
	}
	return gotato.ModelEvent{}, io.EOF
}

func (s *chattyStream) Close() error { return nil }

// A client that stops reading its StreamRun never slows the Run: the adapter
// reads the Run's Subscription, so only the client's own stream waits. The Run
// settles and is saved with every stored Event while the client reads nothing,
// and the client then reads all of them, once and in order, from the stream.
func TestSlowStreamClientNeverSlowsTheRun(t *testing.T) {
	store := session.NewMemoryStore()
	runner, err := service.New(service.Config{Store: store, Agents: []service.Agent{{Name: "chatty", Model: chattyModel{deltas: 1500}}}})
	if err != nil {
		t.Fatal(err)
	}
	listener := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer()
	New(runner).Register(grpcServer)
	go func() { _ = grpcServer.Serve(listener) }()
	connection, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = connection.Close()
		grpcServer.Stop()
	})
	client := gotatov2.NewSessionServiceClient(connection)

	ctx := context.Background()
	s, err := runner.CreateSession(ctx, "chatty", nil)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.StreamRun(ctx, &gotatov2.RunRequest{SessionId: s.ID(), Input: &gotatov2.RunRequest_Prompt{Prompt: "talk"}})
	if err != nil {
		t.Fatal(err)
	}
	// The client reads nothing. The Run must still settle and be saved.
	deadline := time.Now().Add(10 * time.Second)
	for {
		view, err := runner.Session(ctx, s.ID())
		if err != nil {
			t.Fatal(err)
		}
		runs := view.Runs()
		if len(runs) == 1 && runs[0].Status == gotato.RunCompleted && runner.ActiveRuns() == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the Run did not settle while its client stopped reading: runs %+v, active %d", runs, runner.ActiveRuns())
		}
		time.Sleep(10 * time.Millisecond)
	}
	stored, err := session.StoredEvents(ctx, store, s.ID(), 0)
	if err != nil {
		t.Fatal(err)
	}

	// Now the client reads: every stored Event of the Run arrives once, in
	// order, although its stream fell behind; only deltas may be missing.
	var numbers []uint64
	var result *gotatov2.RunResult
	for {
		update, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if event := update.GetEvent(); event != nil {
			switch KindOf(event) {
			case "text_delta", "reasoning_delta", "tool_progress":
			default:
				numbers = append(numbers, event.GetHeader().GetNumber())
			}
		}
		if r := update.GetResult(); r != nil {
			result = r
		}
	}
	if result == nil || result.GetStatus() != string(gotato.RunCompleted) {
		t.Fatalf("result = %v", result)
	}
	var want []uint64
	for _, event := range stored {
		if event.Kind() == gotato.KindSessionReleased {
			continue // after the Run's last Event
		}
		if event.Head().Number >= numbers[0] {
			want = append(want, event.Head().Number)
		}
	}
	if len(numbers) == 0 || len(numbers) > len(want) {
		t.Fatalf("stream numbers %v, stored %v", numbers, want)
	}
	for i, number := range numbers {
		if number != want[i] {
			t.Fatalf("stored Event %d of the stream has number %d, want %d: a gap or a duplicate\nstream %v\nwant   %v", i, number, want[i], numbers, want)
		}
	}
	if len(numbers) != len(want) {
		t.Fatalf("the stream ended after %d stored Events, the Record holds %d", len(numbers), len(want))
	}
}
