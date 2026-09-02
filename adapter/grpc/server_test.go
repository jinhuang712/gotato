package grpcadapter

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/host"
	"github.com/jinhuang712/gotato/internal/testmodel"
	"github.com/jinhuang712/gotato/orchestration"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	gotatov1 "github.com/jinhuang712/gotato/adapter/grpc/gotato/v1"
)

func newTestClient(t *testing.T) (gotatov1.AgentServiceClient, *host.Server) {
	t.Helper()
	o := orchestration.New()
	err := o.Register(orchestration.Definition{Name: "default", New: func(ctx context.Context, request orchestration.Request) (gotato.Agent, error) {
		options := []gotato.Option{gotato.WithModel(testmodel.EchoModel{})}
		return gotato.NewAgent(options...)
	}})
	if err != nil {
		t.Fatal(err)
	}
	hostServer := host.NewServer(o)

	listener := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer()
	New(hostServer).Register(grpcServer)
	go func() { _ = grpcServer.Serve(listener) }()

	connection, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		connection.Close()
		grpcServer.Stop()
		listener.Close()
		hostServer.Drain(context.Background())
	})
	return gotatov1.NewAgentServiceClient(connection), hostServer
}

func TestGRPCReportsTheSameContract(t *testing.T) {
	client, hostServer := newTestClient(t)
	response, err := client.Contract(context.Background(), &gotatov1.ContractRequest{})
	if err != nil {
		t.Fatal(err)
	}
	// Both adapters answer for one contract; a second adapter must not drift
	// into a second version of the semantics.
	if response.GetVersion() != hostServer.Contract() {
		t.Fatalf("contract = %q, host says %q", response.GetVersion(), hostServer.Contract())
	}
}

func TestGRPCRunSettlesThroughTheHostBoundary(t *testing.T) {
	client, _ := newTestClient(t)
	outcome, err := client.Run(context.Background(), &gotatov1.RunCommand{
		AgentName:    "default",
		Conversation: &gotatov1.RunCommand_ConversationKey{ConversationKey: "grpc-run"},
		Input:        &gotatov1.RunCommand_Prompt{Prompt: "hello"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.GetStatus() != string(gotato.RunCompleted) {
		t.Fatalf("outcome = %+v", outcome)
	}
	if outcome.GetFinalMessage() != "echo: hello" {
		t.Fatalf("final message = %q", outcome.GetFinalMessage())
	}
	if outcome.GetConversationId() == "" || outcome.GetAgentId() == "" || outcome.GetRunId() == "" {
		t.Fatalf("outcome lost Core identity: %+v", outcome)
	}
}

func TestGRPCStreamPreservesEventOrderAndEndsWithTheOutcome(t *testing.T) {
	client, _ := newTestClient(t)
	stream, err := client.StreamRun(context.Background(), &gotatov1.RunCommand{
		AgentName:    "default",
		Conversation: &gotatov1.RunCommand_ConversationKey{ConversationKey: "grpc-stream"},
		Input:        &gotatov1.RunCommand_Prompt{Prompt: "hello"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	var sequences []uint64
	var terminal *gotatov1.RunOutcome
	for {
		update, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			t.Fatal(recvErr)
		}
		if event := update.GetEvent(); event != nil {
			if terminal != nil {
				t.Fatal("an Event arrived after the terminal outcome")
			}
			kinds = append(kinds, event.GetKind())
			sequences = append(sequences, event.GetSequence())
			if event.GetAgentId() == "" || event.GetEventClass() == "" {
				t.Fatalf("wire Event lost Core identity or class: %+v", event)
			}
			if event.GetConversationId() == "" {
				t.Fatalf("wire Event lost the routing metadata: %+v", event)
			}
			continue
		}
		terminal = update.GetOutcome()
	}
	if terminal == nil || terminal.GetStatus() != string(gotato.RunCompleted) {
		t.Fatalf("terminal outcome = %+v", terminal)
	}
	if len(kinds) == 0 || kinds[0] != string(gotato.EventAgentStart) || kinds[len(kinds)-1] != string(gotato.EventAgentEnd) {
		t.Fatalf("wire Event order = %v", kinds)
	}
	// Sequence is assigned by Core and must survive the wire unchanged.
	for i := 1; i < len(sequences); i++ {
		if sequences[i] <= sequences[i-1] {
			t.Fatalf("wire Event sequence is not strictly increasing: %v", sequences)
		}
	}
	if sequences[0] != 1 {
		t.Fatalf("first sequence = %d, want 1", sequences[0])
	}
}

func TestGRPCRejectsAnAmbiguousCommand(t *testing.T) {
	client, _ := newTestClient(t)
	_, err := client.Run(context.Background(), &gotatov1.RunCommand{
		AgentName:    "default",
		Conversation: &gotatov1.RunCommand_ConversationKey{ConversationKey: "grpc-bad"},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("command with no input = %v", err)
	}
}



func TestGRPCConversationRecordCarriesNoTranscript(t *testing.T) {
	client, _ := newTestClient(t)
	outcome, err := client.Run(context.Background(), &gotatov1.RunCommand{
		AgentName:    "default",
		Conversation: &gotatov1.RunCommand_ConversationKey{ConversationKey: "grpc-opaque"},
		Input:        &gotatov1.RunCommand_Prompt{Prompt: "secret words"},
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err := client.GetConversation(context.Background(), &gotatov1.ConversationRequest{ConversationId: outcome.GetConversationId()})
	if err != nil {
		t.Fatal(err)
	}
	// The wire record is routing only, the same as the HTTP one.
	if strings.Contains(record.String(), "secret words") {
		t.Fatalf("wire record leaked the transcript: %s", record.String())
	}
}
