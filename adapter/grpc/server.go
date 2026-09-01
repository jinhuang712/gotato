// Package grpcadapter exposes a Host through gRPC.
//
// It lives in its own Go module on purpose. Infrastructure is external and
// replaceable, and an embedded caller that only wants one Agent in its own
// process should not inherit a gRPC dependency to get it. Importing this
// adapter is how a deployment opts in.
//
// The adapter defines no Agent semantics. Every RPC maps onto host.Service,
// which is the one boundary both protocol adapters share.
package grpcadapter

import (
	"context"
	"encoding/json"
	"errors"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/host"
	"github.com/jinhuang712/gotato/orchestration"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gotatov1 "github.com/jinhuang712/gotato/adapter/grpc/gotato/v1"
)

// Server adapts host.Service onto the generated gRPC service.
type Server struct {
	gotatov1.UnimplementedAgentServiceServer
	service host.Service
}

// New wraps a Host service.
func New(service host.Service) *Server {
	return &Server{service: service}
}

// Register attaches this adapter to a gRPC server.
func (s *Server) Register(registrar grpc.ServiceRegistrar) {
	gotatov1.RegisterAgentServiceServer(registrar, s)
}

func (s *Server) Contract(ctx context.Context, _ *gotatov1.ContractRequest) (*gotatov1.ContractResponse, error) {
	return &gotatov1.ContractResponse{Version: s.service.Contract()}, nil
}

func (s *Server) Run(ctx context.Context, request *gotatov1.RunCommand) (*gotatov1.RunOutcome, error) {
	outcome, err := s.service.Run(ctx, commandOf(request))
	if err != nil {
		return nil, statusOf(err)
	}
	return outcomeOf(outcome), nil
}

// streamSink forwards projected Events onto the gRPC stream. A send failure
// ends delivery; the Run keeps its own settlement.
type streamSink struct {
	stream gotatov1.AgentService_StreamRunServer
}

func (s streamSink) Deliver(ctx context.Context, projected host.ProjectedEvent) error {
	payload, err := json.Marshal(projected.Event.Payload)
	if err != nil {
		payload = nil
	}
	return s.stream.Send(&gotatov1.RunUpdate{
		Update: &gotatov1.RunUpdate_Event{Event: &gotatov1.RunEvent{
			AgentId:         string(projected.Event.AgentID),
			RunId:           string(projected.Event.RunID),
			Sequence:        projected.Event.Sequence,
			Kind:            string(projected.Event.Kind),
			EventClass:      string(projected.Event.Class),
			Turn:            uint32(projected.Event.Turn),
			MessageId:       string(projected.Event.MessageID),
			ToolCallId:      string(projected.Event.ToolCallID),
			SpawnId:         string(projected.Event.SpawnID),
			OriginRunId:     string(projected.Event.OriginRunID),
			PayloadJson:     payload,
			ConversationId:  projected.ConversationID,
			AgentGeneration: projected.AgentGeneration,
		}},
	})
}

func (s *Server) StreamRun(request *gotatov1.RunCommand, stream gotatov1.AgentService_StreamRunServer) error {
	outcome, err := s.service.StreamRun(stream.Context(), commandOf(request), streamSink{stream: stream})
	if err != nil {
		return statusOf(err)
	}
	// The terminal outcome is the last message on the stream, so a client
	// reading to the end always has the settled result.
	return stream.Send(&gotatov1.RunUpdate{Update: &gotatov1.RunUpdate_Outcome{Outcome: outcomeOf(outcome)}})
}

func (s *Server) CancelRun(ctx context.Context, request *gotatov1.CancelRunRequest) (*gotatov1.CancelRunResponse, error) {
	if err := s.service.CancelRun(ctx, request.GetRunId()); err != nil {
		return nil, statusOf(err)
	}
	return &gotatov1.CancelRunResponse{}, nil
}

func (s *Server) GetConversation(ctx context.Context, request *gotatov1.ConversationRequest) (*gotatov1.ConversationRecord, error) {
	record, err := s.service.Conversation(ctx, request.GetConversationId())
	if err != nil {
		return nil, statusOf(err)
	}
	return recordOf(record), nil
}

func (s *Server) RetireConversation(ctx context.Context, request *gotatov1.RetireConversationRequest) (*gotatov1.ConversationRecord, error) {
	record, err := s.service.RetireConversation(ctx, request.GetConversationId(), request.GetPolicy())
	if err != nil {
		return nil, statusOf(err)
	}
	return recordOf(record), nil
}

func (s *Server) CloseAgent(ctx context.Context, request *gotatov1.CloseAgentRequest) (*gotatov1.CloseAgentResponse, error) {
	if err := s.service.CloseAgent(ctx, request.GetAgentId()); err != nil {
		return nil, statusOf(err)
	}
	return &gotatov1.CloseAgentResponse{}, nil
}

func commandOf(request *gotatov1.RunCommand) host.Command {
	command := host.Command{
		AgentName:       request.GetAgentName(),
		ConversationID:  request.GetConversationId(),
		ConversationKey: request.GetConversationKey(),
		Retirement:      request.GetRetirement(),
	}
	if request.GetContinueInput() != nil {
		command.Continue = true
	} else {
		command.Prompt = request.GetPrompt()
	}
	if request.ExpectedGeneration != nil {
		generation := request.GetExpectedGeneration()
		command.ExpectedGeneration = &generation
	}
	return command
}

func outcomeOf(outcome host.Outcome) *gotatov1.RunOutcome {
	wire := &gotatov1.RunOutcome{
		ConversationId:  outcome.ConversationID,
		AgentId:         outcome.AgentID,
		AgentGeneration: outcome.AgentGeneration,
		RunId:           outcome.RunID,
		Status:          string(outcome.Status),
		FinalMessage:    outcome.FinalMessage,
	}
	if outcome.Usage != nil {
		wire.Usage = &gotatov1.Usage{
			InputTokens:  outcome.Usage.InputTokens,
			OutputTokens: outcome.Usage.OutputTokens,
			TotalTokens:  outcome.Usage.TotalTokens,
		}
	}
	if outcome.Metrics != nil {
		wire.Metrics = &gotatov1.RunMetrics{
			ElapsedMs:      outcome.Metrics.ElapsedMS,
			Turns:          outcome.Metrics.Turns,
			ToolCalls:      outcome.Metrics.ToolCalls,
			TextBytes:      outcome.Metrics.TextBytes,
			ReasoningBytes: outcome.Metrics.ReasoningBytes,
		}
	}
	if outcome.Error != nil {
		wire.Error = &gotatov1.RuntimeError{
			Code:      string(outcome.Error.Code),
			Operation: outcome.Error.Operation,
			Message:   outcome.Error.Message,
		}
	}
	return wire
}

func recordOf(record orchestration.Record) *gotatov1.ConversationRecord {
	return &gotatov1.ConversationRecord{
		ConversationId:  string(record.ID),
		ConversationKey: string(record.Key),
		AgentName:       string(record.AgentName),
		LiveAgentId:     string(record.LiveAgentID),
		AgentGeneration: uint64(record.Generation),
		Status:          string(record.Status),
		StateVersion:    record.StateVersion,
	}
}

// statusOf maps a Core error code onto a gRPC status. The adapter owns the
// protocol representation; it does not invent new failure meanings.
func statusOf(err error) error {
	if err == nil {
		return nil
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
