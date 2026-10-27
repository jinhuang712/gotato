package grpcadapter

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	gotatov1 "github.com/jinhuang712/gotato/adapter/grpc/gotato/v1"
)

// clientFor serves runner over an in-memory connection.
func clientFor(t *testing.T, runner *service.Runner) gotatov1.SessionServiceClient {
	t.Helper()
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
	return gotatov1.NewSessionServiceClient(connection)
}

func withSessionID(ctx context.Context, id string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, SessionIDKey, id)
}

// The session-id metadata must agree with the Session an RPC is about, and an
// RPC that creates a Session answers with the new ID in its header metadata.
func TestSessionIDMetadataMustAgreeWithTheRequest(t *testing.T) {
	runner, err := service.New(service.Config{Store: session.NewMemoryStore(), Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}}})
	if err != nil {
		t.Fatal(err)
	}
	client := clientFor(t, runner)
	ctx := context.Background()
	a, err := runner.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := runner.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}

	wrong := withSessionID(ctx, b.ID())
	if _, err := client.GetSession(wrong, &gotatov1.SessionRequest{SessionId: a.ID()}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("GetSession with metadata for another Session = %v, want InvalidArgument", err)
	}
	if _, err := client.Run(wrong, &gotatov1.RunRequest{SessionId: a.ID(), Input: &gotatov1.RunRequest_Prompt{Prompt: "hi"}}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("Run with metadata for another Session = %v, want InvalidArgument", err)
	}
	stream, err := client.StreamRun(wrong, &gotatov1.RunRequest{SessionId: a.ID(), Input: &gotatov1.RunRequest_Prompt{Prompt: "hi"}})
	if err == nil {
		_, err = stream.Recv()
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("StreamRun with metadata for another Session = %v, want InvalidArgument", err)
	}
	if _, err := client.CreateSession(withSessionID(ctx, "mine"), &gotatov1.CreateSessionRequest{Id: "yours"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("CreateSession with metadata for another ID = %v, want InvalidArgument", err)
	}
	if loaded, err := runner.Session(ctx, a.ID()); err != nil || len(loaded.Snapshot().Runs) != 0 {
		t.Fatalf("a refused RPC changed the Session: %v", err)
	}

	// The same ID is accepted.
	if _, err := client.GetSession(withSessionID(ctx, a.ID()), &gotatov1.SessionRequest{SessionId: a.ID()}); err != nil {
		t.Fatalf("GetSession with matching metadata: %v", err)
	}

	// RPCs that create a Session set the header.
	var header metadata.MD
	created, err := client.CreateSession(ctx, &gotatov1.CreateSessionRequest{}, grpc.Header(&header))
	if err != nil || len(header.Get(SessionIDKey)) != 1 || header.Get(SessionIDKey)[0] != created.GetId() {
		t.Fatalf("CreateSession header = %v for %v, %v", header, created.GetId(), err)
	}
	header = nil
	result, err := client.Run(ctx, &gotatov1.RunRequest{Input: &gotatov1.RunRequest_Prompt{Prompt: "hi"}}, grpc.Header(&header))
	if err != nil || len(header.Get(SessionIDKey)) != 1 || header.Get(SessionIDKey)[0] != result.GetSessionId() {
		t.Fatalf("Run header = %v for %v, %v", header, result.GetSessionId(), err)
	}
	header = nil
	fork, err := client.ForkSession(ctx, &gotatov1.SessionRequest{SessionId: a.ID()}, grpc.Header(&header))
	if err != nil || len(header.Get(SessionIDKey)) != 1 || header.Get(SessionIDKey)[0] != fork.GetId() {
		t.Fatalf("ForkSession header = %v for %v, %v", header, fork.GetId(), err)
	}
}

// A full instance answers UNAVAILABLE marked "not run" in its trailers, creates
// nothing, and a load balancer retries it elsewhere.
func TestActivationLimitAnswersUnavailableMarkedNotRun(t *testing.T) {
	gate := make(chan struct{})
	model := testkit.NewFakeModel(testkit.Text("done"))
	model.Block = gate
	runner, err := service.New(service.Config{
		Store:     session.NewMemoryStore(),
		Agents:    []service.Agent{{Name: "gated", Model: model}},
		Admission: service.Admission{MaxActiveSessions: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	client := clientFor(t, runner)
	held := make(chan struct{})
	go func() {
		defer close(held)
		_, _ = runner.Run(context.Background(), service.RunRequest{Prompt: "hold"})
	}()
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
		<-held
	})
	deadline := time.Now().Add(5 * time.Second)
	for model.Calls() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	ctx := context.Background()
	before, _ := runner.Sessions(ctx)

	var trailer metadata.MD
	_, err = client.Run(ctx, &gotatov1.RunRequest{Input: &gotatov1.RunRequest_Prompt{Prompt: "new"}}, grpc.Trailer(&trailer))
	if status.Code(err) != codes.Unavailable || len(trailer.Get(NotRunKey)) != 1 || trailer.Get(NotRunKey)[0] != "true" {
		t.Fatalf("Run on a full instance = %v, trailer %v; want UNAVAILABLE marked not run", err, trailer)
	}

	// The streaming call fails the same way, with the mark in its trailer.
	stream, err := client.StreamRun(ctx, &gotatov1.RunRequest{Input: &gotatov1.RunRequest_Prompt{Prompt: "new"}})
	if err != nil {
		t.Fatal(err)
	}
	var end error
	for end == nil {
		_, end = stream.Recv()
	}
	if status.Code(end) != codes.Unavailable || len(stream.Trailer().Get(NotRunKey)) != 1 {
		t.Fatalf("StreamRun on a full instance ended with %v, trailer %v; want UNAVAILABLE marked not run", end, stream.Trailer())
	}

	after, _ := runner.Sessions(ctx)
	if len(after) != len(before) {
		t.Fatalf("Sessions before %d, after %d: a refusal created one", len(before), len(after))
	}
}
