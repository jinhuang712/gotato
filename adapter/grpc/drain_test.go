package grpcadapter

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	gotatov2 "github.com/jinhuang712/gotato/adapter/grpc/gotato/v2"
)

// A Run the drain stops at its deadline sends its result, and its stream
// then ends with UNAVAILABLE and a retry-after-ms trailer, once its Session
// is saved and released.
func TestDrainEndsAStoppedStreamWithUnavailable(t *testing.T) {
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
	stream, err := client.StreamRun(ctx, &gotatov2.RunRequest{SessionId: s.ID(), Input: &gotatov2.RunRequest_Prompt{Prompt: "wait"}})
	if err != nil {
		t.Fatal(err)
	}
	for {
		update, err := stream.Recv()
		if err != nil {
			t.Fatalf("the stream ended before context_built: %v", err)
		}
		if update.GetEvent().GetKind() == string(gotato.EventContextBuilt) {
			break
		}
	}
	drainCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	go func() { _ = runner.Drain(drainCtx) }()

	var result *gotatov2.RunResult
	var end error
	for end == nil {
		var update *gotatov2.RunUpdate
		update, end = stream.Recv()
		if r := update.GetResult(); r != nil {
			result = r
		}
	}
	if result == nil || result.GetStatus() != string(gotato.RunCanceled) {
		t.Fatalf("result = %v, want the cancelled Run", result)
	}
	if status.Code(end) != codes.Unavailable {
		t.Fatalf("the stream ended with %v, want UNAVAILABLE", end)
	}
	values := stream.Trailer().Get("retry-after-ms")
	if len(values) != 1 {
		t.Fatalf("trailer = %v, want one retry-after-ms", stream.Trailer())
	}
	if ms, err := strconv.ParseInt(values[0], 10, 64); err != nil || ms <= 0 {
		t.Fatalf("retry-after-ms = %q", values[0])
	}
	loaded, err := runner.Store().Load(ctx, s.ID())
	if err != nil || loaded.Summary.Busy {
		t.Fatalf("the stopped Run's Session = %+v, %v; want released", loaded.Summary, err)
	}
}
