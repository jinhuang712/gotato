package grpcadapter

import (
	"context"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato/gotato-core"
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

// gate holds the first reply of a Run until released.
type gate struct {
	started chan struct{}
	release chan struct{}
}

func newGate() *gate { return &gate{started: make(chan struct{}, 4), release: make(chan struct{})} }

func (g *gate) Stream(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	if len(request.Messages) == 1 {
		g.started <- struct{}{}
		select {
		case <-g.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return testkit.NewFakeModel(testkit.Text("reply to "+strings.TrimSpace(testkit.UserText(request)))).Stream(ctx, request)
}

// peers are the in-memory listeners instances dial each other through.
type peers struct {
	mu        sync.Mutex
	listeners map[string]*bufconn.Listener
}

func (p *peers) dial(_ context.Context, address string) (net.Conn, error) {
	p.mu.Lock()
	listener := p.listeners[strings.TrimPrefix(address, "passthrough:///")]
	p.mu.Unlock()
	return listener.Dial()
}

type node struct {
	runner *service.Runner
	server *Server
	client gotatov1.SessionServiceClient
	calls  atomic.Int64
}

// newNode starts an instance on the shared store. Its advertised address is
// "passthrough:///<name>", which the peers dial through memory.
func newNode(t *testing.T, store session.Store, mesh *peers, name string, model gotato.Model, forward bool) *node {
	t.Helper()
	n := &node{}
	var err error
	n.runner, err = service.New(service.Config{
		Store: store, Instance: name, Address: "passthrough:///" + name,
		Liveness: service.Liveness{TTL: time.Second, Wait: 3 * time.Second},
		Agents:   []service.Agent{{Name: "echo", Model: model}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var options []Option
	if forward {
		options = append(options, WithForwarding(grpc.WithContextDialer(mesh.dial), grpc.WithTransportCredentials(insecure.NewCredentials())))
	}
	n.server = New(n.runner, options...)
	listener := bufconn.Listen(1 << 20)
	mesh.mu.Lock()
	mesh.listeners[name] = listener
	mesh.mu.Unlock()
	count := grpc.UnaryInterceptor(func(ctx context.Context, request any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		n.calls.Add(1)
		return handler(ctx, request)
	})
	grpcServer := grpc.NewServer(count)
	n.server.Register(grpcServer)
	go func() { _ = grpcServer.Serve(listener) }()
	connection, err := grpc.NewClient("passthrough:///"+name,
		grpc.WithContextDialer(mesh.dial),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = connection.Close()
		grpcServer.Stop()
		_ = n.server.Close()
		_ = n.runner.Close(context.Background())
	})
	n.client = gotatov1.NewSessionServiceClient(connection)
	return n
}

func newPeers() *peers { return &peers{listeners: map[string]*bufconn.Listener{}} }

// running starts a Run on the node that the model holds.
func running(t *testing.T, n *node, g *gate) (string, func()) {
	t.Helper()
	s, err := n.runner.CreateSession(context.Background(), "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = n.runner.Run(context.Background(), service.RunRequest{SessionID: s.ID(), Prompt: "hello"})
	}()
	select {
	case <-g.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the Run never reached the model")
	}
	return s.ID(), func() {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the Run did not settle")
		}
	}
}

func transcriptOf(t *testing.T, n *node, id string) string {
	t.Helper()
	record, err := n.runner.Session(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var parts []string
	for _, m := range record.Messages() {
		parts = append(parts, string(m.Role)+":"+gotato.TextOf(m))
	}
	return strings.Join(parts, "|")
}

func TestSendMessageQueuesForTheRunInFlight(t *testing.T) {
	store, mesh := session.NewMemoryStore(), newPeers()
	g := newGate()
	a := newNode(t, store, mesh, "instance-a", g, false)
	id, wait := running(t, a, g)

	response, err := a.client.SendMessage(context.Background(), &gotatov1.SendMessageRequest{SessionId: id, Kind: "interrupt", Text: "change course"})
	if err != nil || !response.GetQueued() || response.GetMessageId() == "" || response.GetRun() != nil {
		t.Fatalf("SendMessage = %v, %v; want queued with an ID", response, err)
	}
	close(g.release)
	wait()
	if got := transcriptOf(t, a, id); !strings.Contains(got, "user:change course") {
		t.Fatalf("transcript = %q, want the message", got)
	}

	// An invalid message is refused before it runs.
	_, err = a.client.SendMessage(context.Background(), &gotatov1.SendMessageRequest{SessionId: id, Kind: "shout", Text: "x"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("a bad kind = %v, want INVALID_ARGUMENT", err)
	}
}

func TestSendMessageForARunElsewhereIsNotWriter(t *testing.T) {
	store, mesh := session.NewMemoryStore(), newPeers()
	g := newGate()
	a := newNode(t, store, mesh, "instance-a", g, false)
	b := newNode(t, store, mesh, "instance-b", testkit.EchoModel{}, false)
	id, wait := running(t, a, g)

	check := func(name string, trailer metadata.MD, err error) {
		t.Helper()
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("%s = %v, want FAILED_PRECONDITION", name, err)
		}
		if trailer.Get(NotRunKey)[0] != "true" || trailer.Get(RetryableKey)[0] != "true" {
			t.Fatalf("%s trailer = %v, want not run and retryable", name, trailer)
		}
		if got := trailer.Get(WriterKey); len(got) != 1 || got[0] != "passthrough:///instance-a" {
			t.Fatalf("%s trailer = %v, want the writer's address", name, trailer)
		}
	}
	var trailer metadata.MD
	_, err := b.client.SendMessage(context.Background(), &gotatov1.SendMessageRequest{SessionId: id, Text: "from b"}, grpc.Trailer(&trailer))
	check("SendMessage", trailer, err)
	trailer = nil
	_, err = b.client.CancelRun(context.Background(), &gotatov1.CancelRunRequest{Target: &gotatov1.CancelRunRequest_SessionId{SessionId: id}}, grpc.Trailer(&trailer))
	check("CancelRun", trailer, err)

	close(g.release)
	wait()
	if got := transcriptOf(t, a, id); got != "user:hello|assistant:reply to hello" {
		t.Fatalf("transcript = %q; the refused messages must leave nothing", got)
	}
	// With no Run going, b takes the Session over and runs the message.
	response, err := b.client.SendMessage(context.Background(), &gotatov1.SendMessageRequest{SessionId: id, Text: "now from b"})
	if err != nil || response.GetQueued() || response.GetRun().GetStatus() != "completed" {
		t.Fatalf("SendMessage with no Run going = %v, %v; want a new Run", response, err)
	}
}

func TestForwardingSendsTheCallToTheWriter(t *testing.T) {
	store, mesh := session.NewMemoryStore(), newPeers()
	g := newGate()
	a := newNode(t, store, mesh, "instance-a", g, false)
	b := newNode(t, store, mesh, "instance-b", testkit.EchoModel{}, true)
	id, wait := running(t, a, g)
	before := a.calls.Load()

	response, err := b.client.SendMessage(context.Background(), &gotatov1.SendMessageRequest{SessionId: id, Text: "via b"})
	if err != nil || !response.GetQueued() {
		t.Fatalf("forwarded SendMessage = %v, %v; want the writer's queued answer", response, err)
	}
	if a.calls.Load() != before+1 {
		t.Fatalf("the writer saw %d calls, want 1 forwarded", a.calls.Load()-before)
	}
	// A Stop is forwarded too.
	if _, err := b.client.CancelRun(context.Background(), &gotatov1.CancelRunRequest{Target: &gotatov1.CancelRunRequest_SessionId{SessionId: id}}); err != nil {
		t.Fatalf("forwarded CancelRun = %v", err)
	}
	wait()
	// Both took effect on the writer: the message was queued, and the Run
	// stopped (the gate never opened).
	events, err := a.runner.Events(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var queued, aborted bool
	for _, event := range events {
		queued = queued || event.Kind() == gotato.KindMessageQueued
		aborted = aborted || event.Kind() == gotato.KindAbortRequested
	}
	if !queued || !aborted {
		t.Fatalf("message_queued %v, abort_requested %v; want both on the writer", queued, aborted)
	}
}

func TestAForwardedCallIsNeverForwardedAgain(t *testing.T) {
	store, mesh := session.NewMemoryStore(), newPeers()
	g := newGate()
	a := newNode(t, store, mesh, "instance-a", g, false)
	b := newNode(t, store, mesh, "instance-b", testkit.EchoModel{}, true)
	id, wait := running(t, a, g)
	before := a.calls.Load()

	ctx := metadata.AppendToOutgoingContext(context.Background(), ForwardedKey, "true")
	var trailer metadata.MD
	_, err := b.client.SendMessage(ctx, &gotatov1.SendMessageRequest{SessionId: id, Text: "again"}, grpc.Trailer(&trailer))
	if status.Code(err) != codes.FailedPrecondition || len(trailer.Get(WriterKey)) != 1 {
		t.Fatalf("a forwarded call = %v, trailer %v; want not_writer", err, trailer)
	}
	if a.calls.Load() != before {
		t.Fatalf("the writer saw %d calls, want none: the call was forwarded again", a.calls.Load()-before)
	}
	close(g.release)
	wait()
}
