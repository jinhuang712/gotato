package grpcadapter

import (
	"context"
	"net"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	gotatov2 "github.com/jinhuang712/gotato/adapter/grpc/gotato/v2"
)

// hangingModel streams nothing until its call ends, as a slow provider does.
type hangingModel struct{}

func (hangingModel) Stream(context.Context, gotato.ModelRequest) (gotato.ModelStream, error) {
	return hangingStream{}, nil
}

type hangingStream struct{}

func (hangingStream) Recv(ctx context.Context) (gotato.ModelEvent, error) {
	<-ctx.Done()
	return gotato.ModelEvent{}, ctx.Err()
}

func (hangingStream) Close() error { return nil }

// A client that disconnects mid-stream still gets its Run settled and saved
// (GOALS.md G-G02).
func TestStreamDisconnectSettlesAndSavesTheRun(t *testing.T) {
	runner, err := service.New(service.Config{Store: session.NewMemoryStore(), Agents: []service.Agent{{Name: "slow", Model: hangingModel{}}}})
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
	s, err := runner.CreateSession(ctx, "slow", nil)
	if err != nil {
		t.Fatal(err)
	}
	clientCtx, disconnect := context.WithCancel(ctx)
	defer disconnect()
	stream, err := client.StreamRun(clientCtx, &gotatov2.RunRequest{SessionId: s.ID(), Input: &gotatov2.RunRequest_Prompt{Prompt: "hello?"}})
	if err != nil {
		t.Fatal(err)
	}
	for {
		update, err := stream.Recv()
		if err != nil {
			t.Fatalf("the stream ended before context_built: %v", err)
		}
		if KindOf(update.GetEvent()) == "context_built" {
			break
		}
	}
	disconnect()

	deadline := time.Now().Add(5 * time.Second)
	for {
		view, err := runner.Session(ctx, s.ID())
		if err != nil {
			t.Fatal(err)
		}
		runs := view.Runs()
		if len(runs) == 1 && runs[0].Status != gotato.RunRunning && runner.ActiveRuns() == 0 {
			if runs[0].Status != gotato.RunCanceled {
				t.Fatalf("run = %+v, want cancelled", runs[0])
			}
			if messages := view.Messages(); len(messages) != 1 || gotato.TextOf(messages[0]) != "hello?" {
				t.Fatalf("messages = %+v, want the prompt kept", messages)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the Run did not settle and save after the client left: runs %+v, active %d", runs, runner.ActiveRuns())
		}
		time.Sleep(20 * time.Millisecond)
	}
}
