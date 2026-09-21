package grpcadapter

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"testing"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	gotatov2 "github.com/jinhuang712/gotato/adapter/grpc/gotato/v2"
)

func newTestClient(t *testing.T) gotatov2.SessionServiceClient {
	t.Helper()
	runner, err := service.New(service.Config{
		Store: session.NewMemoryStore(),
		Specs: []service.AgentSpec{
			{Name: "echo", Model: testkit.EchoModel{}, ModelName: "echo"},
			{Name: "demo", Model: testkit.DemoModel{}, Tools: []gotato.Tool{testkit.DemoEchoTool()}},
		},
	})
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
	return gotatov2.NewSessionServiceClient(connection)
}

func TestContractAndAgents(t *testing.T) {
	client := newTestClient(t)
	contract, err := client.Contract(context.Background(), &gotatov2.ContractRequest{})
	if err != nil || contract.GetVersion() != ContractVersion {
		t.Fatalf("contract = %v err=%v", contract, err)
	}
	agents, err := client.Agents(context.Background(), &gotatov2.AgentsRequest{})
	if err != nil || len(agents.GetAgents()) != 2 || agents.GetAgents()[0] != "echo" {
		t.Fatalf("agents = %v err=%v", agents, err)
	}
}

func TestRunOverGRPC(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()
	created, err := client.CreateSession(ctx, &gotatov2.CreateSessionRequest{Agent: "demo", Metadata: map[string]string{"k": "v"}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Run(ctx, &gotatov2.RunRequest{SessionId: created.GetId(), Input: &gotatov2.RunRequest_Prompt{Prompt: "use-tool"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.GetStatus() != "completed" || result.GetMetrics().GetToolCalls() != 1 || result.GetMessages() != 4 || result.GetFinalText() != "demo response: use-tool" {
		t.Fatalf("result = %v", result)
	}
	doc, err := client.GetSession(ctx, &gotatov2.SessionRequest{SessionId: created.GetId()})
	if err != nil {
		t.Fatal(err)
	}
	var document session.Document
	if err := json.Unmarshal(doc.GetDocumentJson(), &document); err != nil || len(document.Messages) != 4 || document.Metadata["k"] != "v" {
		t.Fatalf("document = %+v err=%v", document, err)
	}
	report, err := client.Context(ctx, &gotatov2.SessionRequest{SessionId: created.GetId()})
	if err != nil || report.GetPrefixHash() == "" || report.GetSelectedMessages() != 4 {
		t.Fatalf("context = %v err=%v", report, err)
	}
	oneShot, err := client.Run(ctx, &gotatov2.RunRequest{Input: &gotatov2.RunRequest_Prompt{Prompt: "hi"}})
	if err != nil || oneShot.GetSessionId() == "" || oneShot.GetAgent() != "echo" {
		t.Fatalf("one-shot = %v err=%v", oneShot, err)
	}
	list, err := client.ListSessions(ctx, &gotatov2.ListSessionsRequest{})
	if err != nil || len(list.GetSessions()) != 2 {
		t.Fatalf("list = %v err=%v", list, err)
	}
}

func TestStreamRunEventsAndCompactFork(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()
	stream, err := client.StreamRun(ctx, &gotatov2.RunRequest{Agent: "demo", Input: &gotatov2.RunRequest_Prompt{Prompt: "use-tool"}})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
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
			kinds = append(kinds, event.GetKind())
		}
		if r := update.GetResult(); r != nil {
			result = r
		}
	}
	if result == nil || kinds[0] != "agent_start" || kinds[len(kinds)-1] != "agent_end" {
		t.Fatalf("kinds=%v result=%v", kinds, result)
	}
	id := result.GetSessionId()
	for _, prompt := range []string{"a", "b"} {
		if _, err := client.Run(ctx, &gotatov2.RunRequest{SessionId: id, Input: &gotatov2.RunRequest_Prompt{Prompt: prompt}}); err != nil {
			t.Fatal(err)
		}
	}
	events, err := client.Events(ctx, &gotatov2.EventsRequest{SessionId: id, Kind: "context_built"})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for {
		if _, err := events.Recv(); err != nil {
			break
		}
		count++
	}
	if count != 4 {
		t.Fatalf("context_built events = %d, want 4 (2 turns + 1 + 1)", count)
	}
	compact, err := client.Compact(ctx, &gotatov2.CompactRequest{SessionId: id, Keep: 2})
	if err != nil || !compact.GetReplaced() || compact.GetMessagesAfter() != 3 {
		t.Fatalf("compact = %v err=%v", compact, err)
	}
	fork, err := client.ForkSession(ctx, &gotatov2.SessionRequest{SessionId: id})
	if err != nil || fork.GetParentId() != id || fork.GetMessages() != 3 {
		t.Fatalf("fork = %v err=%v", fork, err)
	}
	if _, err := client.DeleteSession(ctx, &gotatov2.SessionRequest{SessionId: fork.GetId()}); err != nil {
		t.Fatal(err)
	}
}

func TestErrorMapping(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()
	cases := []struct {
		name string
		call func() error
		code codes.Code
	}{
		{"missing session", func() error {
			_, err := client.GetSession(ctx, &gotatov2.SessionRequest{SessionId: "missing"})
			return err
		}, codes.NotFound},
		{"unknown agent", func() error {
			_, err := client.Run(ctx, &gotatov2.RunRequest{Agent: "nope", Input: &gotatov2.RunRequest_Prompt{Prompt: "x"}})
			return err
		}, codes.InvalidArgument},
		{"empty prompt", func() error {
			_, err := client.Run(ctx, &gotatov2.RunRequest{})
			return err
		}, codes.InvalidArgument},
		{"cancel inactive", func() error {
			_, err := client.CancelRun(ctx, &gotatov2.CancelRunRequest{Target: &gotatov2.CancelRunRequest_RunId{RunId: "nope"}})
			return err
		}, codes.FailedPrecondition},
		{"session already exists", func() error {
			if _, err := client.CreateSession(ctx, &gotatov2.CreateSessionRequest{Agent: "echo", Id: "grpc-dup"}); err != nil {
				return err
			}
			_, err := client.CreateSession(ctx, &gotatov2.CreateSessionRequest{Agent: "echo", Id: "grpc-dup"})
			return err
		}, codes.AlreadyExists},
	}
	for _, tc := range cases {
		err := tc.call()
		if status.Code(err) != tc.code {
			t.Errorf("%s: code = %v (%v), want %v", tc.name, status.Code(err), err, tc.code)
		}
	}
}

func TestStatusOfMapsContextErrors(t *testing.T) {
	if code := status.Code(statusOf(context.Canceled)); code != codes.Canceled {
		t.Fatalf("canceled = %v", code)
	}
	if code := status.Code(statusOf(context.DeadlineExceeded)); code != codes.DeadlineExceeded {
		t.Fatalf("deadline = %v", code)
	}
}

func TestStreamRunCanceledContextMapsToCanceled(t *testing.T) {
	client := newTestClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stream, err := client.StreamRun(ctx, &gotatov2.RunRequest{Input: &gotatov2.RunRequest_Prompt{Prompt: "x"}})
	if err == nil {
		_, err = stream.Recv()
	}
	if status.Code(err) != codes.Canceled {
		t.Fatalf("code = %v (%v)", status.Code(err), err)
	}
}
