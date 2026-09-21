// Package grpcadapter exposes a service.Runner over gRPC. It is a protocol
// adapter: every RPC maps onto one Runner method and defines no Agent or
// Session semantics of its own.
//
//	runner, _ := service.New(cfg)
//	server := grpc.NewServer()
//	grpcadapter.New(runner).Register(server)
package grpcadapter

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gotatov2 "github.com/jinhuang712/gotato/adapter/grpc/gotato/v2"
)

// ContractVersion is the gRPC contract version.
const ContractVersion = "2"

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
	return &gotatov2.AgentsResponse{Agents: s.runner.Agents()}, nil
}

func (s *Server) CreateSession(ctx context.Context, request *gotatov2.CreateSessionRequest) (*gotatov2.SessionSummary, error) {
	var options []session.Option
	if request.GetId() != "" {
		if _, err := s.runner.Store().Get(ctx, request.GetId()); err == nil {
			return nil, status.Error(codes.AlreadyExists, "session already exists")
		}
		options = append(options, session.WithID(request.GetId()))
	}
	created, err := s.runner.CreateSession(ctx, request.GetAgent(), request.GetMetadata(), options...)
	if err != nil {
		return nil, statusOf(err)
	}
	return summaryOf(session.SummaryOf(created)), nil
}

func (s *Server) GetSession(ctx context.Context, request *gotatov2.SessionRequest) (*gotatov2.SessionDocument, error) {
	found, err := s.runner.Store().Get(ctx, request.GetSessionId())
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
	list, err := s.runner.Store().List(ctx)
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
	if err := s.runner.Store().Delete(ctx, request.GetSessionId()); err != nil {
		return nil, statusOf(err)
	}
	return &gotatov2.DeleteSessionResponse{Deleted: true}, nil
}

func (s *Server) ForkSession(ctx context.Context, request *gotatov2.SessionRequest) (*gotatov2.SessionSummary, error) {
	child, err := s.runner.Fork(ctx, request.GetSessionId())
	if err != nil {
		return nil, statusOf(err)
	}
	return summaryOf(session.SummaryOf(child)), nil
}

func (s *Server) Run(ctx context.Context, request *gotatov2.RunRequest) (*gotatov2.RunResult, error) {
	result, err := s.runner.Run(ctx, runRequestOf(request))
	if err != nil && result.SessionID == "" {
		return nil, statusOf(err)
	}
	return resultOf(result), nil
}

func (s *Server) StreamRun(request *gotatov2.RunRequest, stream gotatov2.SessionService_StreamRunServer) error {
	sink := func(event gotato.Event) error {
		return stream.Send(&gotatov2.RunUpdate{Update: &gotatov2.RunUpdate_Event{Event: eventOf(event)}})
	}
	result, err := s.runner.StreamRun(stream.Context(), runRequestOf(request), sink)
	if err != nil && result.SessionID == "" {
		return statusOf(err)
	}
	return stream.Send(&gotatov2.RunUpdate{Update: &gotatov2.RunUpdate_Result{Result: resultOf(result)}})
}

func (s *Server) CancelRun(ctx context.Context, request *gotatov2.CancelRunRequest) (*gotatov2.CancelRunResponse, error) {
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
	found, err := s.runner.Store().Get(stream.Context(), request.GetSessionId())
	if err != nil {
		return statusOf(err)
	}
	for _, event := range found.Events() {
		if request.GetKind() != "" && string(event.Kind) != request.GetKind() {
			continue
		}
		if err := stream.Send(eventOf(event)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) Context(ctx context.Context, request *gotatov2.SessionRequest) (*gotatov2.ContextReport, error) {
	report, err := s.runner.Inspect(ctx, request.GetSessionId())
	if err != nil {
		return nil, statusOf(err)
	}
	data, _ := json.Marshal(report)
	return &gotatov2.ContextReport{
		SessionId:        report.SessionID,
		PrefixHash:       report.PrefixHash,
		ApproxTokens:     int32(report.ApproxTokens),
		SelectedMessages: int32(report.SelectedMessages),
		ReportJson:       data,
	}, nil
}

func (s *Server) Compact(ctx context.Context, request *gotatov2.CompactRequest) (*gotatov2.CompactResult, error) {
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
		compaction, _ = json.Marshal(result.Compaction)
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
		Usage: &gotatov2.Usage{
			InputTokens:  result.Result.Usage.InputTokens,
			OutputTokens: result.Result.Usage.OutputTokens,
			TotalTokens:  result.Result.Usage.TotalTokens,
		},
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
		Usage:     &gotatov2.Usage{InputTokens: summary.Usage.InputTokens, OutputTokens: summary.Usage.OutputTokens, TotalTokens: summary.Usage.TotalTokens},
		Metadata:  summary.Metadata,
	}
}

func eventOf(event gotato.Event) *gotatov2.Event {
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		payload = nil
	}
	return &gotatov2.Event{
		AgentId:     string(event.AgentID),
		RunId:       string(event.RunID),
		Sequence:    event.Sequence,
		Kind:        string(event.Kind),
		EventClass:  string(event.Class),
		Turn:        uint32(event.Turn),
		MessageId:   string(event.MessageID),
		ToolCallId:  string(event.ToolCallID),
		PayloadJson: payload,
		Timestamp:   event.Timestamp.UTC().Format(time.RFC3339Nano),
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
	var runtimeErr *gotato.RuntimeError
	code := codes.Internal
	if errors.As(err, &runtimeErr) {
		switch runtimeErr.Code {
		case gotato.ErrInvalidArgument, gotato.ErrToolArgumentFailure:
			code = codes.InvalidArgument
		case gotato.ErrBusy, gotato.ErrInvalidState, gotato.ErrAgentClosing:
			code = codes.FailedPrecondition
		case gotato.ErrAgentClosed:
			code = codes.NotFound
		case gotato.ErrCancelled:
			code = codes.Canceled
		case gotato.ErrDeadlineExceeded:
			code = codes.DeadlineExceeded
		case gotato.ErrLimitExceeded:
			code = codes.ResourceExhausted
		case gotato.ErrNotSupported:
			code = codes.Unimplemented
		}
		return status.Error(code, runtimeErr.Error())
	}
	return status.Error(code, err.Error())
}
