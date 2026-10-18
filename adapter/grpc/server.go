// Package grpcadapter exposes a service.Runner over gRPC. It is a protocol
// adapter: every RPC maps onto one Runner method, and Agent and Session
// semantics stay in the runtime.
//
//	runner, _ := service.New(cfg)
//	server := grpc.NewServer()
//	grpcadapter.New(runner).Register(server)
package grpcadapter

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	gotatov2 "github.com/jinhuang712/gotato/adapter/grpc/gotato/v2"
)

// ContractVersion is the gRPC contract version. It counts breaking changes to
// what the service returns, JSON payloads included; the protobuf package
// stays gotato.v2 until its schema itself breaks.
const ContractVersion = "4"

// SessionIDKey is the metadata key that names the Session an RPC is about. A
// load balancer hashes on it to send every call for a Session to one instance,
// its writer. The server reads it as a check: it must agree with the Session ID
// in the request, or the call fails with INVALID_ARGUMENT, since the load
// balancer would have routed it for another Session. An RPC that creates a
// Session answers with the new ID in its header metadata under the same key,
// for the client to send next time. StreamRun cannot: its headers go out before
// the Session exists, and its first Event names it.
const SessionIDKey = "session-id"

// NotRunKey is the trailer metadata that marks an UNAVAILABLE whose call did
// not run, so a load balancer may send it again to another instance without
// running it twice. Its value is "true".
const NotRunKey = "gotato-not-run"

// checkSessionID fails when the call's session-id metadata names another
// Session than id. Absent or blank metadata agrees with all.
func checkSessionID(ctx context.Context, id string) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok || id == "" {
		return nil
	}
	for _, value := range md.Get(SessionIDKey) {
		if value = strings.TrimSpace(value); value != "" && value != id {
			return status.Errorf(codes.InvalidArgument, "the %s metadata names session %q, the request is about session %q", SessionIDKey, value, id)
		}
	}
	return nil
}

// announceSession sets the header metadata of a call that created a Session.
func announceSession(ctx context.Context, id string) {
	_ = grpc.SetHeader(ctx, metadata.Pairs(SessionIDKey, id))
}

// notRun reports whether err refused a call before it ran anything.
func notRun(err error) bool { return gotato.IsCode(err, gotato.ErrOverloaded) }

// Server implements gotatov2.SessionServiceServer.
type Server struct {
	gotatov2.UnimplementedSessionServiceServer
	runner *service.Runner
}

// New creates a Server over runner.
func New(runner *service.Runner) *Server { return &Server{runner: runner} }

// Register registers the service on a gRPC server.
func (s *Server) Register(registrar grpc.ServiceRegistrar) {
	gotatov2.RegisterSessionServiceServer(registrar, s)
}

func (s *Server) Contract(context.Context, *gotatov2.ContractRequest) (*gotatov2.ContractResponse, error) {
	return &gotatov2.ContractResponse{Version: ContractVersion}, nil
}

func (s *Server) Agents(context.Context, *gotatov2.AgentsRequest) (*gotatov2.AgentsResponse, error) {
	names := s.runner.Agents()
	caps := map[string]string{}
	for _, name := range names {
		if spec, ok := s.runner.Agent(name); ok && spec.MaxEffect != gotato.EffectUnspecified {
			caps[name] = string(spec.MaxEffect)
		}
	}
	return &gotatov2.AgentsResponse{Agents: names, MaxEffect: caps}, nil
}

func (s *Server) CreateSession(ctx context.Context, request *gotatov2.CreateSessionRequest) (*gotatov2.SessionSummary, error) {
	if err := checkSessionID(ctx, request.GetId()); err != nil {
		return nil, err
	}
	if request.GetId() != "" {
		created, err := s.runner.CreateSessionExclusive(ctx, request.GetAgent(), request.GetId(), request.GetMetadata())
		if errors.Is(err, service.ErrSessionExists) {
			return nil, status.Error(codes.AlreadyExists, "session already exists")
		}
		if err != nil {
			return nil, statusOf(err)
		}
		announceSession(ctx, created.ID())
		return summaryOf(session.SummaryOf(created)), nil
	}
	created, err := s.runner.CreateSession(ctx, request.GetAgent(), request.GetMetadata())
	if err != nil {
		return nil, statusOf(err)
	}
	announceSession(ctx, created.ID())
	return summaryOf(session.SummaryOf(created)), nil
}

func (s *Server) GetSession(ctx context.Context, request *gotatov2.SessionRequest) (*gotatov2.SessionDocument, error) {
	if err := checkSessionID(ctx, request.GetSessionId()); err != nil {
		return nil, err
	}
	found, err := s.runner.Session(ctx, request.GetSessionId())
	if err != nil {
		return nil, statusOf(err)
	}
	data, err := json.Marshal(found.Snapshot())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &gotatov2.SessionDocument{DocumentJson: data}, nil
}

func (s *Server) ListSessions(ctx context.Context, _ *gotatov2.ListSessionsRequest) (*gotatov2.ListSessionsResponse, error) {
	list, err := s.runner.Sessions(ctx)
	if err != nil {
		return nil, statusOf(err)
	}
	out := &gotatov2.ListSessionsResponse{}
	for _, summary := range list {
		out.Sessions = append(out.Sessions, summaryOf(summary))
	}
	return out, nil
}

func (s *Server) DeleteSession(ctx context.Context, request *gotatov2.SessionRequest) (*gotatov2.DeleteSessionResponse, error) {
	if err := checkSessionID(ctx, request.GetSessionId()); err != nil {
		return nil, err
	}
	if err := s.runner.DeleteSession(ctx, request.GetSessionId()); err != nil {
		return nil, statusOf(err)
	}
	return &gotatov2.DeleteSessionResponse{Deleted: true}, nil
}

func (s *Server) ForkSession(ctx context.Context, request *gotatov2.SessionRequest) (*gotatov2.SessionSummary, error) {
	if err := checkSessionID(ctx, request.GetSessionId()); err != nil {
		return nil, err
	}
	child, err := s.runner.Fork(ctx, request.GetSessionId(), "")
	if err != nil {
		return nil, statusOf(err)
	}
	announceSession(ctx, child.ID())
	return summaryOf(session.SummaryOf(child)), nil
}

func (s *Server) Run(ctx context.Context, request *gotatov2.RunRequest) (*gotatov2.RunResult, error) {
	if err := checkSessionID(ctx, request.GetSessionId()); err != nil {
		return nil, err
	}
	result, err := s.runner.Run(ctx, runRequestOf(request))
	if err != nil && result.SessionID == "" {
		if notRun(err) {
			_ = grpc.SetTrailer(ctx, metadata.Pairs(NotRunKey, "true"))
		}
		return nil, statusOf(err)
	}
	if errors.Is(err, service.ErrNotPersisted) {
		return nil, notPersisted(err)
	}
	if request.GetSessionId() == "" {
		announceSession(ctx, result.SessionID)
	}
	return resultOf(result), nil
}

func (s *Server) StreamRun(request *gotatov2.RunRequest, stream gotatov2.SessionService_StreamRunServer) error {
	if err := checkSessionID(stream.Context(), request.GetSessionId()); err != nil {
		return err
	}
	// The Runner reads the Run's Subscription and calls sink from this
	// goroutine, so a client that reads slowly slows only this stream.
	sink := func(event gotato.Event) error {
		converted, err := eventOf(event)
		if err != nil {
			return err
		}
		return stream.Send(&gotatov2.RunUpdate{Update: &gotatov2.RunUpdate_Event{Event: converted}})
	}
	result, err := s.runner.StreamRun(stream.Context(), runRequestOf(request), sink)
	var end error
	switch {
	case err != nil && result.SessionID == "":
		if notRun(err) {
			stream.SetTrailer(metadata.Pairs(NotRunKey, "true"))
		}
		end = statusOf(err)
	case errors.Is(err, service.ErrNotPersisted):
		end = notPersisted(err)
	default:
		end = stream.Send(&gotatov2.RunUpdate{Update: &gotatov2.RunUpdate_Result{Result: resultOf(result)}})
	}
	if result.Reconnect == nil {
		return end
	}
	// The drain stopped the Run: the stream ends with UNAVAILABLE, and says
	// how long to wait before the Session is continued on any instance.
	stream.SetTrailer(metadata.Pairs("retry-after-ms", strconv.FormatInt(result.Reconnect.RetryAfterMS, 10)))
	return status.Error(codes.Unavailable, "gotato: this instance is draining; reconnect to continue the session")
}

func (s *Server) CancelRun(ctx context.Context, request *gotatov2.CancelRunRequest) (*gotatov2.CancelRunResponse, error) {
	if err := checkSessionID(ctx, request.GetSessionId()); err != nil {
		return nil, err
	}
	var err error
	if id := request.GetSessionId(); id != "" {
		err = s.runner.CancelSession(ctx, id)
	} else {
		err = s.runner.CancelRun(ctx, gotato.RunID(request.GetRunId()))
	}
	if err != nil {
		return nil, statusOf(err)
	}
	return &gotatov2.CancelRunResponse{}, nil
}

func (s *Server) Events(request *gotatov2.EventsRequest, stream gotatov2.SessionService_EventsServer) error {
	if err := checkSessionID(stream.Context(), request.GetSessionId()); err != nil {
		return err
	}
	events, err := s.runner.Events(stream.Context(), request.GetSessionId())
	if err != nil {
		return statusOf(err)
	}
	for _, event := range events {
		if request.GetKind() != "" && string(event.Kind()) != request.GetKind() {
			continue
		}
		converted, err := eventOf(event)
		if err != nil {
			return err
		}
		if err := stream.Send(converted); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) Context(ctx context.Context, request *gotatov2.SessionRequest) (*gotatov2.ContextReport, error) {
	if err := checkSessionID(ctx, request.GetSessionId()); err != nil {
		return nil, err
	}
	report, err := s.runner.Inspect(ctx, request.GetSessionId())
	if err != nil {
		return nil, statusOf(err)
	}
	data, err := json.Marshal(report)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &gotatov2.ContextReport{
		SessionId:        report.SessionID,
		PrefixHash:       report.PrefixHash,
		ApproxTokens:     int32(report.ApproxTokens),
		SelectedMessages: int32(report.SelectedMessages),
		ReportJson:       data,
	}, nil
}

func (s *Server) Compact(ctx context.Context, request *gotatov2.CompactRequest) (*gotatov2.CompactResult, error) {
	if err := checkSessionID(ctx, request.GetSessionId()); err != nil {
		return nil, err
	}
	keep := int(request.GetKeep())
	if keep <= 0 {
		keep = 4
	}
	result, err := s.runner.Compact(ctx, request.GetSessionId(), modelctx.CompactOptions{Keep: keep})
	if err != nil {
		return nil, statusOf(err)
	}
	var compaction []byte
	if result.Compaction != nil {
		compaction, err = json.Marshal(result.Compaction)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	}
	return &gotatov2.CompactResult{
		SessionId:      result.SessionID,
		Replaced:       result.Replaced,
		MessagesBefore: int32(result.MessagesBefore),
		MessagesAfter:  int32(result.MessagesAfter),
		TokensBefore:   int32(result.TokensBefore),
		TokensAfter:    int32(result.TokensAfter),
		CompactionJson: compaction,
	}, nil
}

// ---- mapping ----------------------------------------------------------------

func runRequestOf(request *gotatov2.RunRequest) service.RunRequest {
	out := service.RunRequest{SessionID: request.GetSessionId(), Agent: request.GetAgent(), Metadata: request.GetMetadata()}
	if request.GetContinueInput() != nil {
		out.Continue = true
	} else {
		out.Prompt = request.GetPrompt()
	}
	return out
}

func resultOf(result service.RunResult) *gotatov2.RunResult {
	wire := &gotatov2.RunResult{
		SessionId: result.SessionID,
		Agent:     result.Agent,
		Model:     result.Model,
		RunId:     string(result.Result.RunID),
		Status:    string(result.Result.Status),
		FinalText: result.FinalText,
		Usage:     usageOf(result.Result.Usage),
		Metrics: &gotatov2.RunMetrics{
			ElapsedMs:      result.Result.Metrics.ElapsedMS,
			Turns:          result.Result.Metrics.Turns,
			ToolCalls:      result.Result.Metrics.ToolCalls,
			TextBytes:      result.Result.Metrics.TextBytes,
			ReasoningBytes: result.Result.Metrics.ReasoningBytes,
		},
		Compacted: result.Compacted,
		Messages:  int32(result.Messages),
		Events:    int32(result.Events),
	}
	if result.Result.Error != nil {
		wire.Error = &gotatov2.RuntimeError{
			Code:      string(result.Result.Error.Code),
			Operation: result.Result.Error.Operation,
			Message:   result.Result.Error.Message,
		}
	}
	return wire
}

func summaryOf(summary session.Summary) *gotatov2.SessionSummary {
	return &gotatov2.SessionSummary{
		Id:        summary.ID,
		ParentId:  summary.ParentID,
		CreatedAt: summary.CreatedAt,
		UpdatedAt: summary.UpdatedAt,
		Messages:  int32(summary.Messages),
		Runs:      int32(summary.Runs),
		Usage:     usageOf(summary.Usage),
		Metadata:  summary.Metadata,
	}
}

// statusOf maps a runtime error onto a gRPC status without inventing new
// failure meanings.
func statusOf(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, session.ErrNotFound) {
		return status.Error(codes.NotFound, err.Error())
	}
	// A caller that cancelled or exceeded its deadline gets CANCELED /
	// DEADLINE_EXCEEDED, not INTERNAL.
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, err.Error())
	}
	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, err.Error())
	}
	var runtimeErr *gotato.RuntimeError
	code := codes.Internal
	if errors.As(err, &runtimeErr) {
		switch runtimeErr.Code {
		case gotato.ErrInvalidArgument, gotato.ErrToolArgumentFailure:
			code = codes.InvalidArgument
		case gotato.ErrBusy, gotato.ErrInvalidState, gotato.ErrSessionClosing:
			code = codes.FailedPrecondition
		case gotato.ErrSessionClosed:
			code = codes.NotFound
		case gotato.ErrCancelled:
			code = codes.Canceled
		case gotato.ErrDeadlineExceeded:
			code = codes.DeadlineExceeded
		case gotato.ErrLimitExceeded:
			code = codes.ResourceExhausted
		case gotato.ErrNotSupported:
			code = codes.Unimplemented
		case gotato.ErrConflict:
			code = codes.Aborted
		case gotato.ErrStoreFailure, gotato.ErrOverloaded:
			code = codes.Unavailable
		}
		return status.Error(code, runtimeErr.Error())
	}
	return status.Error(code, err.Error())
}

// notPersisted reports a Run whose result was not kept: Aborted when another
// writer took the Session during the Run, Internal otherwise.
func notPersisted(err error) error {
	if errors.Is(err, session.ErrConflict) {
		return status.Error(codes.Aborted, err.Error())
	}
	return status.Error(codes.Internal, err.Error())
}
