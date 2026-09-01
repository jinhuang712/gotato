package host

import (
	"context"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/orchestration"
)

// ContractVersion identifies the frozen Host semantic contract. A protocol
// adapter reports it so a client can tell which command and outcome shapes it
// is talking to.
const ContractVersion = "1"

// Command is one request against the Host boundary. It is protocol neutral:
// an adapter decodes its wire format into this and encodes Outcome back.
//
// Exactly one of Prompt or Continue is the input. Start semantics belong to
// the adapter's stream lifecycle, not here: this type is a single command,
// and a stream that sends two of them is making a protocol error its own
// adapter must reject.
type Command struct {
	AgentName          string
	ConversationID     string
	ConversationKey    string
	ExpectedGeneration *uint64
	Prompt             string
	// Continue runs the Loop without appending a user Message. Prompt must be
	// empty when it is set.
	Continue   bool
	Retirement string
}

// Outcome is the settled result of a Command, projected for delivery. It
// preserves Core identity and terminal meaning; it carries no transcript.
type Outcome struct {
	ConversationID  string
	AgentID         string
	AgentGeneration uint64
	RunID           string
	Status          gotato.RunStatus
	FinalMessage    string
	Usage           *gotato.Usage
	Metrics         *gotato.RunMetrics
	Error           *gotato.RuntimeError
}

// EventSink receives projected Events during a streaming Run. Returning an
// error stops delivery; it does not by itself cancel the Run, because
// execution settlement and delivery settlement are separate.
type EventSink interface {
	Deliver(ctx context.Context, event ProjectedEvent) error
}

// ProjectedEvent is a Core Event plus the routing metadata Orchestration adds.
// The Core fields keep their meaning: an adapter may re-encode them but may
// not redefine order, class, or correlation.
type ProjectedEvent struct {
	ConversationID  string
	AgentGeneration uint64
	Event           gotato.Event
}

// Service is the protocol-independent Host boundary. Every protocol adapter
// maps onto this one interface, which is what keeps a second adapter from
// growing a second set of semantics.
type Service interface {
	// Contract reports the frozen contract version this Service implements.
	Contract() string
	// Run settles one Command and returns its Outcome.
	Run(ctx context.Context, command Command) (Outcome, error)
	// StreamRun settles one Command while delivering its Events to sink.
	StreamRun(ctx context.Context, command Command, sink EventSink) (Outcome, error)
	// CancelRun asks the owning Agent to abort a Run. It is idempotent while
	// the Run is active; after settlement it reports that the Run is not
	// active rather than pretending to have cancelled it.
	CancelRun(ctx context.Context, runID string) error
	// Conversation reports one routing record.
	Conversation(ctx context.Context, id string) (orchestration.Record, error)
	// RetireConversation closes the live Agent under the given policy. It is a
	// lifecycle operation, never a side effect of a stream ending.
	RetireConversation(ctx context.Context, id string, policy string) (orchestration.Record, error)
	// CloseAgent closes one live Core Agent.
	CloseAgent(ctx context.Context, agentID string) error
	// Ready reports whether the Host still admits work.
	Ready() bool
	// Drain stops admission and settles what is running.
	Drain(ctx context.Context) error
}

// Server implements Service. The assertion is here so a change to either side
// fails at build time rather than at the second adapter.
var _ Service = (*Server)(nil)

func (s *Server) Contract() string { return ContractVersion }

func (s *Server) Ready() bool { return s.ready.Load() }

func (c Command) toOrchestration() (orchestration.Request, error) {
	if c.Prompt != "" && c.Continue {
		return orchestration.Request{}, gotato.ErrorOf(gotato.ErrInvalidArgument, "a command carries either a Prompt or a Continue, not both")
	}
	if c.Prompt == "" && !c.Continue {
		return orchestration.Request{}, gotato.ErrorOf(gotato.ErrInvalidArgument, "a command needs a Prompt or a Continue")
	}
	name := c.AgentName
	if name == "" {
		name = "default"
	}
	request := orchestration.Request{
		AgentName:       gotato.AgentName(name),
		ConversationID:  gotato.ConversationID(c.ConversationID),
		ConversationKey: gotato.ConversationKey(c.ConversationKey),
		Retirement:      orchestration.RetirementPolicy(c.Retirement),
	}
	if c.ExpectedGeneration != nil {
		generation := gotato.AgentGeneration(*c.ExpectedGeneration)
		request.ExpectedGeneration = &generation
	}
	return request, nil
}

func outcomeOf(record orchestration.Record, result gotato.RunResult, err error) Outcome {
	response := responseFor(record, result, err)
	return Outcome{
		ConversationID:  response.ConversationID,
		AgentID:         response.AgentID,
		AgentGeneration: response.AgentGeneration,
		RunID:           response.RunID,
		Status:          response.RunStatus,
		FinalMessage:    response.FinalMessage,
		Usage:           response.Usage,
		Metrics:         response.Metrics,
		Error:           response.Error,
	}
}

// Run settles one Command through Orchestration.
func (s *Server) Run(ctx context.Context, command Command) (Outcome, error) {
	if !s.ready.Load() {
		return Outcome{}, gotato.ErrorOf(gotato.ErrInvalidState, "host is draining")
	}
	request, err := command.toOrchestration()
	if err != nil {
		return Outcome{}, err
	}
	result, record, dispatchErr := s.Orchestration.Dispatch(ctx, request, gotato.UserMessage(command.Prompt))
	return outcomeOf(record, result, dispatchErr), dispatchErr
}

// StreamRun settles one Command while delivering its Events. Delivery failure
// ends delivery only: whether it also cancels the Run is the Host's declared
// policy, carried by CancelRunOnDisconnect.
func (s *Server) StreamRun(ctx context.Context, command Command, sink EventSink) (Outcome, error) {
	if !s.ready.Load() {
		return Outcome{}, gotato.ErrorOf(gotato.ErrInvalidState, "host is draining")
	}
	request, err := command.toOrchestration()
	if err != nil {
		return Outcome{}, err
	}
	agent, record, err := s.Orchestration.Resolve(ctx, request)
	if err != nil {
		return outcomeOf(record, gotato.RunResult{}, err), err
	}
	source, ok := agent.(gotato.EventSource)
	if !ok {
		return Outcome{}, gotato.ErrorOf(gotato.ErrNotSupported, "Agent does not expose Events")
	}
	stream, err := source.Subscribe(ctx)
	if err != nil {
		return Outcome{}, err
	}
	defer stream.Close()

	runCtx := ctx
	if !s.CancelRunOnDisconnect {
		runCtx = context.Background()
	}
	resultCh := make(chan dispatchResponse, 1)
	go func() {
		result, finalRecord, dispatchErr := s.Orchestration.Dispatch(runCtx, request, gotato.UserMessage(command.Prompt))
		resultCh <- dispatchResponse{result: result, record: finalRecord, err: dispatchErr}
	}()

	for {
		event, nextErr := stream.Next(ctx)
		if nextErr != nil {
			response := <-resultCh
			return outcomeOf(response.record, response.result, response.err), response.err
		}
		projected := ProjectedEvent{
			ConversationID:  string(record.ID),
			AgentGeneration: uint64(record.Generation),
			Event:           event,
		}
		if deliverErr := sink.Deliver(ctx, projected); deliverErr != nil {
			response := <-resultCh
			return outcomeOf(response.record, response.result, response.err), deliverErr
		}
		if event.Kind == gotato.EventAgentEnd {
			response := <-resultCh
			return outcomeOf(response.record, response.result, response.err), response.err
		}
	}
}

func (s *Server) CancelRun(ctx context.Context, runID string) error {
	if runID == "" {
		return gotato.ErrorOf(gotato.ErrInvalidArgument, "Run ID is required")
	}
	return s.Orchestration.CancelRun(ctx, gotato.RunID(runID))
}

func (s *Server) Conversation(ctx context.Context, id string) (orchestration.Record, error) {
	record, ok := s.Orchestration.Get(gotato.ConversationID(id))
	if !ok {
		return orchestration.Record{}, gotato.ErrorOf(gotato.ErrAgentClosed, "conversation not found")
	}
	return record, nil
}

func (s *Server) RetireConversation(ctx context.Context, id string, policy string) (orchestration.Record, error) {
	retirement := orchestration.RetirementPolicy(policy)
	if retirement == "" {
		retirement = orchestration.Retain
	}
	if err := s.Orchestration.Retire(ctx, gotato.ConversationID(id), retirement); err != nil {
		return orchestration.Record{}, err
	}
	record, _ := s.Orchestration.Get(gotato.ConversationID(id))
	return record, nil
}

func (s *Server) CloseAgent(ctx context.Context, agentID string) error {
	return s.Orchestration.CloseAgent(ctx, gotato.AgentID(agentID))
}
