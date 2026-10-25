package grpcadapter

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/jinhuang712/gotato/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	gotatov2 "github.com/jinhuang712/gotato/adapter/grpc/gotato/v2"
)

// WriterKey is the trailer metadata of a not_writer failure: the address the
// writer of the Run advertises. It is absent when the writer advertises none.
const WriterKey = "gotato-writer"

// ForwardedKey is the request metadata that marks a call another instance
// forwarded here (see WithForwarding). Its value is "true". Such a call is
// never forwarded again.
const ForwardedKey = "gotato-forwarded"

// Option configures New.
type Option func(*Server)

// WithForwarding turns on forwarding. A SendMessage or a CancelRun by session
// that reaches this instance while another live instance writes the Run is
// sent, as it came, to the address that instance advertises (a gRPC target
// such as "10.0.0.7:8788"), with the metadata gotato-forwarded, and that
// answer is returned. A call that carries the metadata is never forwarded
// again: it fails with not_writer. dial holds the options to connect with,
// the transport credentials among them; the call carries the caller's
// metadata, so your own authentication applies at the writer. Gotato connects
// to nothing else. Call Close to end the connections.
func WithForwarding(dial ...grpc.DialOption) Option {
	return func(s *Server) {
		s.forward = &forwarder{options: dial, conns: map[string]*grpc.ClientConn{}}
	}
}

// forwarder keeps one connection for each writer address.
type forwarder struct {
	options []grpc.DialOption
	mu      sync.Mutex
	conns   map[string]*grpc.ClientConn
	closed  bool
}

func (f *forwarder) client(address string) (gotatov2.SessionServiceClient, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil, errors.New("grpcadapter: the server is closed")
	}
	conn := f.conns[address]
	if conn == nil {
		var err error
		if conn, err = grpc.NewClient(address, f.options...); err != nil {
			return nil, err
		}
		f.conns[address] = conn
	}
	return gotatov2.NewSessionServiceClient(conn), nil
}

// Close ends the connections that forwarding opened. It does not stop a gRPC
// server, which the caller owns.
func (s *Server) Close() error {
	if s.forward == nil {
		return nil
	}
	s.forward.mu.Lock()
	defer s.forward.mu.Unlock()
	s.forward.closed = true
	var err error
	for _, conn := range s.forward.conns {
		err = errors.Join(err, conn.Close())
	}
	s.forward.conns = map[string]*grpc.ClientConn{}
	return err
}

// forwardedCtx is the context of a forwarded call: the caller's metadata, less
// what gRPC sets by itself, and the forwarded marker.
func forwardedCtx(ctx context.Context) context.Context {
	out := metadata.MD{}
	if incoming, ok := metadata.FromIncomingContext(ctx); ok {
		for key, values := range incoming {
			if strings.HasPrefix(key, ":") || strings.HasPrefix(key, "grpc-") || key == "content-type" || key == "user-agent" || key == "te" {
				continue
			}
			out[key] = values
		}
	}
	out.Set(ForwardedKey, "true")
	return metadata.NewOutgoingContext(ctx, out)
}

// forwardable says whether err calls for forwarding: another instance writes
// the Run, it advertises an address, forwarding is on, and the call was not
// forwarded already.
func (s *Server) forwardable(ctx context.Context, err error) (address string, ok bool) {
	address, notWriter := service.WriterOf(err)
	if !notWriter || address == "" || s.forward == nil {
		return "", false
	}
	if md, has := metadata.FromIncomingContext(ctx); has && len(md.Get(ForwardedKey)) > 0 {
		return "", false
	}
	return address, true
}

// forwardCall sends a call to the writer at address, and returns the writer's
// error with its trailer carried over. The callback makes the call with the
// context and the options it is given.
func (s *Server) forwardCall(ctx context.Context, address string, call func(ctx context.Context, client gotatov2.SessionServiceClient, options ...grpc.CallOption) error) error {
	client, err := s.forward.client(address)
	if err != nil {
		return failed(ctx, err)
	}
	var trailer metadata.MD
	err = call(forwardedCtx(ctx), client, grpc.Trailer(&trailer))
	if err != nil {
		// The writer's answer is returned as it is, with its trailer; a call
		// that never reached it is the connection's, and is not marked not run.
		if trailer.Len() > 0 {
			_ = grpc.SetTrailer(ctx, trailer)
		}
		return err
	}
	return nil
}

// SendMessage implements gotatov2.SessionServiceServer.
func (s *Server) SendMessage(ctx context.Context, request *gotatov2.SendMessageRequest) (*gotatov2.SendMessageResponse, error) {
	if err := checkSessionID(ctx, request.GetSessionId()); err != nil {
		return nil, err
	}
	result, err := s.runner.Send(ctx, service.MessageRequest{
		SessionID: request.GetSessionId(),
		Kind:      service.MessageKind(request.GetKind()),
		Text:      request.GetText(),
	})
	if err != nil && (result.Run == nil || result.Run.SessionID == "") {
		if address, ok := s.forwardable(ctx, err); ok {
			var response *gotatov2.SendMessageResponse
			forwardErr := s.forwardCall(ctx, address, func(ctx context.Context, client gotatov2.SessionServiceClient, options ...grpc.CallOption) error {
				var callErr error
				response, callErr = client.SendMessage(ctx, request, options...)
				return callErr
			})
			return response, forwardErr
		}
		return nil, failed(ctx, err)
	}
	if errors.Is(err, service.ErrNotPersisted) {
		_ = grpc.SetTrailer(ctx, trailerOf(err))
		return nil, notPersisted(err)
	}
	response := &gotatov2.SendMessageResponse{SessionId: result.SessionID, MessageId: result.MessageID, Queued: result.Queued}
	if result.Run != nil {
		response.Run = resultOf(*result.Run)
	}
	return response, nil
}

// Notice implements gotatov2.SessionServiceServer.
func (s *Server) Notice(ctx context.Context, request *gotatov2.NoticeRequest) (*gotatov2.NoticeResponse, error) {
	if err := checkSessionID(ctx, request.GetSessionId()); err != nil {
		return nil, err
	}
	result, err := s.runner.Notice(ctx, service.NoticeRequest{
		SessionID: request.GetSessionId(),
		Kind:      request.GetKind(),
		Text:      request.GetText(),
	})
	if err != nil {
		if address, ok := s.forwardable(ctx, err); ok {
			var response *gotatov2.NoticeResponse
			forwardErr := s.forwardCall(ctx, address, func(ctx context.Context, client gotatov2.SessionServiceClient, options ...grpc.CallOption) error {
				var callErr error
				response, callErr = client.Notice(ctx, request, options...)
				return callErr
			})
			return response, forwardErr
		}
		return nil, failed(ctx, err)
	}
	return &gotatov2.NoticeResponse{SessionId: result.SessionID, Queued: result.Queued, Number: result.Number, MessageId: result.MessageID}, nil
}

// cancelSession stops the Run of a Session, forwarding the call to the writer
// when forwarding is on and another instance writes it.
func (s *Server) cancelSession(ctx context.Context, request *gotatov2.CancelRunRequest, id string) error {
	err := s.runner.CancelSession(ctx, id)
	if err == nil {
		return nil
	}
	if address, ok := s.forwardable(ctx, err); ok {
		return s.forwardCall(ctx, address, func(ctx context.Context, client gotatov2.SessionServiceClient, options ...grpc.CallOption) error {
			_, callErr := client.CancelRun(ctx, request, options...)
			return callErr
		})
	}
	return failed(ctx, err)
}
