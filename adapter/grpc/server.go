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
	"io"
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

// NotRunKey is the trailer metadata that marks a failed call that did
// not run, whatever its status, so a load balancer may send it again to another instance without
// running it twice. Its value is "true".
const NotRunKey = "gotato-not-run"

// RetryableKey is the trailer metadata that marks a failed call that may
// succeed when sent again later. Its value is "true".
const RetryableKey = "gotato-retryable"

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

// trailerOf is the trailer metadata of a failed call: what the runtime says
// about the error, translated and decided nowhere else. NotRunKey is "true"
// when the call did not run, so a load balancer may send it again to another
// instance without running it twice; RetryableKey is "true" when the same
// call, sent again later, may succeed. An error that says neither adds no
// metadata.
func trailerOf(err error) metadata.MD {
	md := metadata.MD{}
	if gotato.NotRun(err) {
		md.Set(NotRunKey, "true")
	}
	if gotato.Retryable(err) {
		md.Set(RetryableKey, "true")
	}
	if writer, ok := service.WriterOf(err); ok && writer != "" {
		md.Set(WriterKey, writer)
	}
	return md
}

// failed ends a unary call with err: its status, and its trailer.
func failed(ctx context.Context, err error) error {
	if md := trailerOf(err); md.Len() > 0 {
		_ = grpc.SetTrailer(ctx, md)
	}
	return statusOf(err)
}

// streamFailed ends a stream with err: its status, and its trailer.
func streamFailed(stream grpc.ServerStream, err error) error {
	if md := trailerOf(err); md.Len() > 0 {
		stream.SetTrailer(md)
	}
	return statusOf(err)
}

// Server implements gotatov2.SessionServiceServer.
type Server struct {
	gotatov2.UnimplementedSessionServiceServer
	runner  *service.Runner
	forward *forwarder
}

// New creates a Server over runner.
func New(runner *service.Runner, options ...Option) *Server {
	s := &Server{runner: runner}
	for _, option := range options {
		if option != nil {
			option(s)
		}
	}
	return s
}

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
			return nil, failed(ctx, err)
		}
		announceSession(ctx, created.ID())
		return summaryOf(session.SummaryOf(created)), nil
	}
	created, err := s.runner.CreateSession(ctx, request.GetAgent(), request.GetMetadata())
	if err != nil {
		return nil, failed(ctx, err)
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
		return nil, failed(ctx, err)
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
		return nil, failed(ctx, err)
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
		return nil, failed(ctx, err)
	}
	return &gotatov2.DeleteSessionResponse{Deleted: true}, nil
}

func (s *Server) ForkSession(ctx context.Context, request *gotatov2.SessionRequest) (*gotatov2.SessionSummary, error) {
	if err := checkSessionID(ctx, request.GetSessionId()); err != nil {
		return nil, err
	}
	child, err := s.runner.Fork(ctx, request.GetSessionId(), "")
	if err != nil {
		return nil, failed(ctx, err)
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
		return nil, failed(ctx, err)
	}
	if errors.Is(err, service.ErrNotPersisted) {
		_ = grpc.SetTrailer(ctx, trailerOf(err))
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
		end = streamFailed(stream, err)
	case errors.Is(err, service.ErrNotPersisted):
		stream.SetTrailer(trailerOf(err))
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

// RecordEpochKey is the metadata key that carries a Record's epoch: in the
// header metadata of StreamSession, and in the trailer of a refusal to resume.
const RecordEpochKey = "record-epoch"

// StreamSession serves a Session's stream: the stored Events after a number,
// read from the Record, and then the live ones. The first message is the
// start, with the Record's epoch; a drain ends the stream with UNAVAILABLE and
// a retry-after-ms trailer, like StreamRun.
func (s *Server) StreamSession(request *gotatov2.StreamSessionRequest, stream gotatov2.SessionService_StreamSessionServer) error {
	if err := checkSessionID(stream.Context(), request.GetSessionId()); err != nil {
		return err
	}
	opened, err := s.runner.StreamSession(stream.Context(), request.GetSessionId(), service.StreamFrom{After: request.GetAfter(), RecordEpoch: request.GetRecordEpoch()})
	if err != nil {
		var runtimeErr *gotato.RuntimeError
		if errors.As(err, &runtimeErr) && runtimeErr.Details["record_epoch"] != "" {
			stream.SetTrailer(metadata.Pairs(RecordEpochKey, runtimeErr.Details["record_epoch"]))
		}
		return streamFailed(stream, err)
	}
	defer func() { _ = opened.Close() }()
	if err := stream.SendHeader(metadata.Pairs(RecordEpochKey, opened.RecordEpoch)); err != nil {
		return err
	}
	start := &gotatov2.StreamStart{SessionId: request.GetSessionId(), RecordEpoch: opened.RecordEpoch, After: request.GetAfter(), Head: opened.Head}
	if err := stream.Send(&gotatov2.SessionUpdate{Update: &gotatov2.SessionUpdate_Start{Start: start}}); err != nil {
		return err
	}
	for {
		event, err := opened.Next(stream.Context())
		switch {
		case err == nil:
			converted, err := eventOf(event)
			if err != nil {
				return err
			}
			if err := stream.Send(&gotatov2.SessionUpdate{Update: &gotatov2.SessionUpdate_Event{Event: converted}}); err != nil {
				return err
			}
		case stream.Context().Err() != nil:
			return statusOf(stream.Context().Err())
		case errors.Is(err, io.EOF):
			hint := opened.Reconnect()
			if hint == nil {
				return nil
			}
			stream.SetTrailer(metadata.Pairs("retry-after-ms", strconv.FormatInt(hint.RetryAfterMS, 10)))
			return status.Error(codes.Unavailable, "gotato: this instance is draining; reconnect to continue the stream")
		default:
			return streamFailed(stream, err)
		}
	}
}

func (s *Server) CancelRun(ctx context.Context, request *gotatov2.CancelRunRequest) (*gotatov2.CancelRunResponse, error) {
	if err := checkSessionID(ctx, request.GetSessionId()); err != nil {
		return nil, err
	}
	if id := request.GetSessionId(); id != "" {
		if err := s.cancelSession(ctx, request, id); err != nil {
			return nil, err
		}
		return &gotatov2.CancelRunResponse{}, nil
	}
	if err := s.runner.CancelRun(ctx, gotato.RunID(request.GetRunId())); err != nil {
		return nil, failed(ctx, err)
	}
	return &gotatov2.CancelRunResponse{}, nil
}

func (s *Server) Events(request *gotatov2.EventsRequest, stream gotatov2.SessionService_EventsServer) error {
	if err := checkSessionID(stream.Context(), request.GetSessionId()); err != nil {
		return err
	}
	events, err := s.runner.Events(stream.Context(), request.GetSessionId())
	if err != nil {
		return streamFailed(stream, err)
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
		return nil, failed(ctx, err)
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
	if request.GetKeep() < 0 || request.GetMinTokens() < 0 {
		return nil, status.Error(codes.InvalidArgument, "keep and min_tokens cannot be negative")
	}
	result, err := s.runner.Compact(ctx, request.GetSessionId(), modelctx.CompactOptions{Keep: int(request.GetKeep()), MinTokens: int(request.GetMinTokens())})
	if err != nil {
		return nil, failed(ctx, err)
	}
	var compaction []byte
	if result.Compaction != nil {
		compaction, err = json.Marshal(result.Compaction)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	}
	var merge []byte
	if result.Merge != nil {
		if merge, err = json.Marshal(result.Merge); err != nil {
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
		Reason:         result.Reason,
		MergeJson:      merge,
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
		Compacted:   result.Compacted,
		Messages:    int32(result.Messages),
		Events:      int32(result.Events),
		RecordEpoch: result.RecordEpoch,
	}
	if result.Result.Error != nil {
		wire.Error = &gotatov2.RuntimeError{
			Code:      string(result.Result.Error.Code),
			Operation: result.Result.Error.Operation,
			Message:   result.Result.Error.Message,
			Retryable: result.Result.Error.Retryable,
			NotRun:    result.Result.Error.NotRun,
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
		// A Record written before epochs names one from its creation time.
		RecordEpoch: summary.Epoch(),
	}
}

// codeOf is the gRPC code of each ErrorCode. Every code is listed, so a new
// one cannot be left to a default by accident; the test over
// gotato.ErrorCodes fails when one is missing.
var codeOf = map[gotato.ErrorCode]codes.Code{
	gotato.ErrInvalidArgument:       codes.InvalidArgument,
	gotato.ErrToolArgumentFailure:   codes.InvalidArgument,
	gotato.ErrNotFound:              codes.NotFound,
	gotato.ErrSessionClosed:         codes.NotFound,
	gotato.ErrBusy:                  codes.FailedPrecondition,
	gotato.ErrInvalidState:          codes.FailedPrecondition,
	gotato.ErrSessionClosing:        codes.FailedPrecondition,
	gotato.ErrCannotResume:          codes.FailedPrecondition,
	gotato.ErrNotWriter:             codes.FailedPrecondition,
	gotato.ErrCancelled:             codes.Canceled,
	gotato.ErrDeadlineExceeded:      codes.DeadlineExceeded,
	gotato.ErrLimitExceeded:         codes.ResourceExhausted,
	gotato.ErrContextOverflow:       codes.ResourceExhausted,
	gotato.ErrNotSupported:          codes.Unimplemented,
	gotato.ErrConflict:              codes.Aborted,
	gotato.ErrStoreFailure:          codes.Unavailable,
	gotato.ErrOverloaded:            codes.Unavailable,
	gotato.ErrDraining:              codes.Unavailable,
	gotato.ErrModelFailure:          codes.Internal,
	gotato.ErrModelProtocolFailure:  codes.Internal,
	gotato.ErrToolResolutionFailure: codes.Internal,
	gotato.ErrToolExecutionFailure:  codes.Internal,
	gotato.ErrExtensionFailure:      codes.Internal,
	gotato.ErrInternalInvariant:     codes.Internal,
}

// statusOf maps a runtime error onto a gRPC status without inventing new
// failure meanings. It translates the error's code; whether the call ran and
// whether a retry may succeed travel in the trailer (trailerOf).
func statusOf(err error) error {
	if err == nil {
		return nil
	}
	var runtimeErr *gotato.RuntimeError
	switch {
	case errors.As(err, &runtimeErr):
		if code, ok := codeOf[runtimeErr.Code]; ok {
			return status.Error(code, runtimeErr.Error())
		}
		return status.Error(codes.Internal, runtimeErr.Error())
	case errors.Is(err, context.DeadlineExceeded):
		// A caller that cancelled or exceeded its deadline gets CANCELED /
		// DEADLINE_EXCEEDED, not INTERNAL.
		return status.Error(codes.DeadlineExceeded, err.Error())
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	}
	return status.Error(codes.Internal, err.Error())
}

// notPersisted reports a Run whose result was not kept: Aborted when another
// writer took the Session during the Run, Internal otherwise.
func notPersisted(err error) error {
	if errors.Is(err, session.ErrConflict) {
		return status.Error(codes.Aborted, err.Error())
	}
	return status.Error(codes.Internal, err.Error())
}
