package gotato

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"runtime/debug"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Agent interface {
	Prompt(context.Context, Message) (RunResult, error)
	Close(context.Context) error
}

type AgentStatus string

const (
	AgentCreated AgentStatus = "created"
	AgentIdle    AgentStatus = "idle"
	AgentBusy    AgentStatus = "busy"
	AgentClosing AgentStatus = "closing"
	AgentClosed  AgentStatus = "closed"
)

type AgentLifecycle interface {
	Agent
	Status() AgentStatus
	Done() <-chan struct{}
}

type EventAgent interface {
	Agent
	Subscribe(context.Context) (EventStream, error)
}

type LifecycleAgent interface {
	Agent
	SubscribeLifecycle(context.Context) (LifecycleStream, error)
}

type Snapshotter interface {
	Snapshot(context.Context) (CoreSnapshot, error)
}

// RunCanceler lets a Host or Orchestration layer cancel an active Run without
// closing the Agent or discarding its Conversation state.
type RunCanceler interface {
	Agent
	CancelRun(context.Context, RunID) error
}

type IdleWaiter interface {
	WaitForIdle(context.Context) error
}

type RetirableAgent interface {
	Agent
	RequestRetirement(context.Context, string) error
}

type Option func(*agentConfig) error

type agentConfig struct {
	model       Model
	instruction string
	tools       []Tool
	limits      CoreLimits
	limitsSet   bool
	initial     CoreSnapshot
	hasInitial  bool
}

func WithModel(model Model) Option {
	return func(c *agentConfig) error {
		if model == nil {
			return runtimeError(ErrInvalidArgument, "WithModel", "model is nil", nil)
		}
		c.model = model
		return nil
	}
}

func WithInstruction(instruction string) Option {
	return func(c *agentConfig) error { c.instruction = instruction; return nil }
}

func WithTool(tool Tool) Option {
	return func(c *agentConfig) error {
		if tool == nil {
			return runtimeError(ErrInvalidArgument, "WithTool", "tool is nil", nil)
		}
		c.tools = append(c.tools, tool)
		return nil
	}
}

func WithTools(tools ...Tool) Option {
	return func(c *agentConfig) error {
		for _, tool := range tools {
			if tool == nil {
				return runtimeError(ErrInvalidArgument, "WithTools", "tool is nil", nil)
			}
		}
		c.tools = append(c.tools, tools...)
		return nil
	}
}

func WithLimits(limits CoreLimits) Option {
	return func(c *agentConfig) error {
		if limits.RunDeadline < 0 || limits.ModelCallDeadline < 0 || limits.ToolCallDeadline < 0 {
			return runtimeError(ErrInvalidArgument, "WithLimits", "deadlines cannot be negative", nil)
		}
		c.limits = limits
		c.limitsSet = true
		return nil
	}
}

// WithDeadlines overrides only the Run, Model, and Tool deadlines. A zero
// duration disables that deadline. Other local limits retain their defaults
// and are not made explicit by this option.
func WithDeadlines(run, model, tool time.Duration) Option {
	return func(c *agentConfig) error {
		if run < 0 || model < 0 || tool < 0 {
			return runtimeError(ErrInvalidArgument, "WithDeadlines", "deadlines cannot be negative", nil)
		}
		c.limits.RunDeadline = run
		c.limits.ModelCallDeadline = model
		c.limits.ToolCallDeadline = tool
		return nil
	}
}

func WithInitialSnapshot(snapshot CoreSnapshot) Option {
	return func(c *agentConfig) error {
		if snapshot.Version == 0 {
			snapshot.Version = 1
		}
		c.initial = snapshot.Clone()
		c.hasInitial = true
		return nil
	}
}

func NewAgent(options ...Option) (Agent, error) {
	cfg := agentConfig{limits: defaultLimits()}
	for _, option := range options {
		if option == nil {
			continue
		}
		if err := option(&cfg); err != nil {
			return nil, err
		}
	}
	if cfg.model == nil {
		return nil, runtimeError(ErrInvalidArgument, "NewAgent", "a Model is required", nil)
	}
	if err := validateLimits(cfg.limits); err != nil {
		return nil, err
	}

	tools := make(map[string]Tool, len(cfg.tools))
	specs := make([]ToolSpec, 0, len(cfg.tools))
	for _, tool := range cfg.tools {
		spec := tool.Spec()
		if err := validateToolSpec(spec); err != nil {
			return nil, err
		}
		if _, exists := tools[spec.ID]; exists {
			return nil, runtimeError(ErrInvalidArgument, "NewAgent", "duplicate Tool ID: "+spec.ID, nil)
		}
		tools[spec.ID] = tool
		if spec.Name != "" {
			if _, exists := tools[spec.Name]; exists {
				return nil, runtimeError(ErrInvalidArgument, "NewAgent", "duplicate Tool name: "+spec.Name, nil)
			}
			tools[spec.Name] = tool
		}
		specs = append(specs, cloneToolSpec(spec))
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].ID < specs[j].ID })

	a := &coreAgent{
		id:           AgentID(nextID("agent")),
		model:        cfg.model,
		instruction:  cfg.instruction,
		tools:        tools,
		toolSpecs:    specs,
		limits:       cfg.limits,
		limitsSet:    cfg.limitsSet,
		commands:     make(chan agentCommand),
		closeSignal:  make(chan struct{}),
		done:         make(chan struct{}),
		ready:        make(chan struct{}),
		admission:    make(chan struct{}, 1),
		events:       newEventHub(),
		lifecycle:    newLifecycleHub(),
		stateChange:  make(chan struct{}),
		messages:     nil,
		stateVersion: 1,
	}
	if cfg.hasInitial {
		a.instruction = cfg.initial.SystemInstructions
		a.messages = cloneMessages(cfg.initial.Messages)
		a.stateVersion = cfg.initial.StateVersion
		if a.stateVersion == 0 {
			a.stateVersion = 1
		}
	}
	a.setStatus(AgentCreated)
	go a.loop()
	<-a.ready
	return a, nil
}

type coreAgent struct {
	id          AgentID
	model       Model
	instruction string
	tools       map[string]Tool
	toolSpecs   []ToolSpec
	limits      CoreLimits
	limitsSet   bool

	commands    chan agentCommand
	closeSignal chan struct{}
	closeOnce   sync.Once
	done        chan struct{}
	doneOnce    sync.Once
	ready       chan struct{}
	admission   chan struct{}

	closeRequested atomic.Bool
	status         atomic.Uint32

	stateMu      sync.Mutex
	stateChange  chan struct{}
	messages     []Message
	stateVersion uint64

	runMu          sync.Mutex
	currentRunID   RunID
	currentRunStop context.CancelFunc

	events    *eventHub
	lifecycle *lifecycleHub
}

type agentCommandKind uint8

const (
	commandPrompt agentCommandKind = iota + 1
	commandSnapshot
)

type agentCommand struct {
	kind     agentCommandKind
	ctx      context.Context
	message  Message
	result   chan promptResponse
	snapshot chan snapshotResponse
}

type promptResponse struct {
	result RunResult
	err    error
}

type snapshotResponse struct {
	snapshot CoreSnapshot
	err      error
}

func (a *coreAgent) ID() AgentID { return a.id }

func (a *coreAgent) Status() AgentStatus {
	status := decodeAgentStatus(a.status.Load())
	if a.closeRequested.Load() && status != AgentClosed {
		return AgentClosing
	}
	return status
}

func (a *coreAgent) Done() <-chan struct{} { return a.done }

func (a *coreAgent) CancelRun(ctx context.Context, runID RunID) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if runID == "" {
		return runtimeError(ErrInvalidArgument, "CancelRun", "Run ID is required", nil)
	}
	a.runMu.Lock()
	currentID := a.currentRunID
	stop := a.currentRunStop
	a.runMu.Unlock()
	if currentID != runID || stop == nil {
		return runtimeError(ErrInvalidState, "CancelRun", "Run is not active", nil)
	}
	stop()
	return nil
}

func (a *coreAgent) Subscribe(ctx context.Context) (EventStream, error) {
	return a.events.subscribe(ctx)
}

func (a *coreAgent) SubscribeLifecycle(ctx context.Context) (LifecycleStream, error) {
	return a.lifecycle.subscribe(ctx)
}

func (a *coreAgent) RequestRetirement(ctx context.Context, reason string) error {
	if ctx != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
	if a.Status() == AgentClosed {
		return nil
	}
	a.lifecycle.publish(LifecycleEvent{Kind: LifecycleAgentRetirementRequested, AgentID: a.id, Reason: boundedReason(reason), Timestamp: time.Now()})
	return nil
}

func (a *coreAgent) Prompt(ctx context.Context, message Message) (RunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validatePrompt(message); err != nil {
		return RunResult{}, err
	}
	if err := a.admissionError(); err != nil {
		return RunResult{}, err
	}

	select {
	case a.admission <- struct{}{}:
	default:
		return RunResult{}, runtimeError(ErrBusy, "Prompt", "Agent is already processing a Run", nil)
	}
	releaseAdmission := true
	defer func() {
		if releaseAdmission {
			<-a.admission
		}
	}()

	cmd := agentCommand{kind: commandPrompt, ctx: ctx, message: message, result: make(chan promptResponse, 1)}
	select {
	case a.commands <- cmd:
		releaseAdmission = false
	case <-ctx.Done():
		return RunResult{}, runtimeError(codeForContext(ctx.Err()), "Prompt", "Prompt was cancelled before admission", ctx.Err())
	case <-a.closeSignal:
		return RunResult{}, a.admissionError()
	}

	select {
	case response := <-cmd.result:
		return response.result, response.err
	case <-ctx.Done():
		return RunResult{}, runtimeError(codeForContext(ctx.Err()), "Prompt", "Prompt context ended", ctx.Err())
	}
}

func (a *coreAgent) admissionError() error {
	switch a.Status() {
	case AgentClosing:
		return runtimeError(ErrAgentClosing, "Prompt", "Agent is closing", nil)
	case AgentClosed:
		return runtimeError(ErrAgentClosed, "Prompt", "Agent is closed", nil)
	default:
		return nil
	}
}

func (a *coreAgent) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	a.closeOnce.Do(func() {
		a.closeRequested.Store(true)
		a.setStatus(AgentClosing)
		a.lifecycle.publish(LifecycleEvent{Kind: LifecycleAgentClosing, AgentID: a.id, Timestamp: time.Now()})
		close(a.closeSignal)
	})
	select {
	case <-a.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *coreAgent) WaitForIdle(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		switch a.Status() {
		case AgentIdle:
			return nil
		case AgentCreated, AgentBusy:
			a.stateMu.Lock()
			changed := a.stateChange
			a.stateMu.Unlock()
			select {
			case <-changed:
			case <-ctx.Done():
				return ctx.Err()
			}
		default:
			return a.admissionError()
		}
	}
}

func (a *coreAgent) Snapshot(ctx context.Context) (CoreSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := a.admissionError(); err != nil {
		return CoreSnapshot{}, err
	}
	if a.Status() != AgentIdle {
		return CoreSnapshot{}, runtimeError(ErrBusy, "Snapshot", "Agent is not idle", nil)
	}
	cmd := agentCommand{kind: commandSnapshot, ctx: ctx, snapshot: make(chan snapshotResponse, 1)}
	select {
	case a.commands <- cmd:
	case <-ctx.Done():
		return CoreSnapshot{}, ctx.Err()
	case <-a.closeSignal:
		return CoreSnapshot{}, a.admissionError()
	}
	select {
	case response := <-cmd.snapshot:
		return response.snapshot, response.err
	case <-ctx.Done():
		return CoreSnapshot{}, ctx.Err()
	}
}

func (a *coreAgent) loop() {
	a.setStatus(AgentIdle)
	a.lifecycle.publish(LifecycleEvent{Kind: LifecycleAgentCreated, AgentID: a.id, Timestamp: time.Now()})
	close(a.ready)
	defer a.finishClose()

	for {
		select {
		case <-a.closeSignal:
			return
		case cmd := <-a.commands:
			switch cmd.kind {
			case commandSnapshot:
				if a.Status() != AgentIdle {
					cmd.snapshot <- snapshotResponse{err: runtimeError(ErrBusy, "Snapshot", "Agent is not idle", nil)}
					continue
				}
				cmd.snapshot <- snapshotResponse{snapshot: a.snapshotUnsafe()}
			case commandPrompt:
				if err := a.admissionError(); err != nil {
					cmd.result <- promptResponse{err: err}
					<-a.admission
					continue
				}
				a.setStatus(AgentBusy)
				result, err := a.executeRun(cmd.ctx, cmd.message)
				cmd.result <- promptResponse{result: result, err: err}
				<-a.admission
				if a.closeRequested.Load() {
					return
				}
				a.setStatus(AgentIdle)
			}
		}
	}
}

func (a *coreAgent) finishClose() {
	a.setStatus(AgentClosed)
	a.lifecycle.publish(LifecycleEvent{Kind: LifecycleAgentClosed, AgentID: a.id, Timestamp: time.Now()})
	a.events.close()
	a.lifecycle.close()
	a.doneOnce.Do(func() { close(a.done) })
}

func (a *coreAgent) setStatus(status AgentStatus) {
	a.status.Store(encodeAgentStatus(status))
	a.stateMu.Lock()
	old := a.stateChange
	a.stateChange = make(chan struct{})
	close(old)
	a.stateMu.Unlock()
}

func encodeAgentStatus(status AgentStatus) uint32 {
	switch status {
	case AgentCreated:
		return 1
	case AgentIdle:
		return 2
	case AgentBusy:
		return 3
	case AgentClosing:
		return 4
	case AgentClosed:
		return 5
	default:
		return 0
	}
}

func decodeAgentStatus(status uint32) AgentStatus {
	switch status {
	case 1:
		return AgentCreated
	case 2:
		return AgentIdle
	case 3:
		return AgentBusy
	case 4:
		return AgentClosing
	case 5:
		return AgentClosed
	default:
		return AgentCreated
	}
}

func (a *coreAgent) snapshotUnsafe() CoreSnapshot {
	return CoreSnapshot{
		Version:            1,
		SystemInstructions: a.instruction,
		Messages:           cloneMessages(a.messages),
		StateVersion:       a.stateVersion,
		CapturedAt:         time.Now(),
	}
}

func (a *coreAgent) commitMessage(message Message) error {
	if message.ID == "" {
		message.ID = MessageID(nextID("message"))
	}
	message = message.Clone()
	bytes, err := json.Marshal(message)
	if err != nil {
		return runtimeError(ErrInternalInvariant, "commitMessage", "cannot encode Message", err)
	}
	if limitExceededUint32(a.limitsSet, a.limits.MaxMessages, uint32(len(a.messages)+1)) {
		return runtimeError(ErrLimitExceeded, "commitMessage", "maximum Messages exceeded", nil)
	}
	if limitExceededUint64(a.limitsSet, a.limits.MaxMessageBytes, uint64(len(bytes))) {
		return runtimeError(ErrLimitExceeded, "commitMessage", "maximum Message bytes exceeded", nil)
	}
	candidate := append(cloneMessages(a.messages), message)
	transcript, err := json.Marshal(candidate)
	if err != nil {
		return runtimeError(ErrInternalInvariant, "commitMessage", "cannot encode transcript", err)
	}
	if limitExceededUint64(a.limitsSet, a.limits.MaxTranscriptBytes, uint64(len(transcript))) {
		return runtimeError(ErrLimitExceeded, "commitMessage", "maximum transcript bytes exceeded", nil)
	}
	a.messages = candidate
	a.stateVersion++
	return nil
}

func (a *coreAgent) emit(runID RunID, seq *uint64, kind EventKind, class EventClass, turn TurnNumber, messageID MessageID, callID ToolCallID, payload map[string]any) {
	*seq++
	a.events.publish(Event{AgentID: a.id, RunID: runID, Sequence: *seq, Kind: kind, Class: class, Turn: turn, MessageID: messageID, ToolCallID: callID, Payload: payload, Timestamp: time.Now()})
}

func (a *coreAgent) executeRun(ctx context.Context, prompt Message) (RunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if a.limits.RunDeadline > 0 {
		var deadlineCancel context.CancelFunc
		runCtx, deadlineCancel = context.WithTimeout(runCtx, a.limits.RunDeadline)
		defer deadlineCancel()
	}

	runID := RunID(nextID("run"))
	a.runMu.Lock()
	a.currentRunID = runID
	a.currentRunStop = cancel
	a.runMu.Unlock()
	defer func() {
		a.runMu.Lock()
		if a.currentRunID == runID {
			a.currentRunID = ""
			a.currentRunStop = nil
		}
		a.runMu.Unlock()
	}()

	var sequence uint64
	a.emit(runID, &sequence, EventAgentStart, EventProtected, 0, "", "", nil)
	fail := func(err *RuntimeError) (RunResult, error) {
		if err == nil {
			err = runtimeError(ErrInternalInvariant, "Run", "nil terminal error", nil)
		}
		result := RunResult{RunID: runID, Status: RunFailed, Error: err}
		if errors.Is(err.Cause, context.Canceled) || err.Code == ErrCancelled {
			result.Status = RunCanceled
		}
		if err.Code == ErrDeadlineExceeded {
			result.Status = RunDeadlineExceeded
		}
		a.emit(runID, &sequence, EventAgentEnd, EventProtected, 0, "", "", map[string]any{"status": result.Status, "error": err.Code})
		return result, err
	}

	prompt.ID = MessageID(nextID("message"))
	if err := a.commitMessage(prompt); err != nil {
		return fail(asRuntimeError(err))
	}
	a.emit(runID, &sequence, EventMessageStart, EventProtected, 0, prompt.ID, "", map[string]any{"role": string(prompt.Role)})
	a.emit(runID, &sequence, EventMessageEnd, EventProtected, 0, prompt.ID, "", map[string]any{"role": string(prompt.Role)})

	var totalUsage Usage
	var final *Message
	var toolCalls uint32
	for turn := TurnNumber(1); ; turn++ {
		if limitExceededUint32(a.limitsSet, a.limits.MaxTurns, uint32(turn)) {
			return fail(runtimeError(ErrLimitExceeded, "Run", "maximum Turns exceeded", nil))
		}
		if err := contextFailure(runCtx, "turn"); err != nil {
			return fail(err)
		}
		a.emit(runID, &sequence, EventTurnStart, EventProtected, turn, "", "", nil)

		request := ModelRequest{SystemInstructions: a.instruction, Messages: cloneMessages(a.messages), Tools: cloneToolSpecs(a.toolSpecs)}
		assistant, usage, modelErr := a.readAssistant(runCtx, runID, &sequence, turn, request)
		totalUsage = usage
		if modelErr != nil {
			return fail(modelErr)
		}
		if len(assistant.ToolCalls) > 0 {
			assistant.StopReason = StopToolCalls
		}
		if err := a.commitMessage(assistant); err != nil {
			return fail(asRuntimeError(err))
		}
		a.emit(runID, &sequence, EventMessageEnd, EventProtected, turn, assistant.ID, "", map[string]any{"tool_calls": len(assistant.ToolCalls)})

		if len(assistant.ToolCalls) == 0 {
			a.emit(runID, &sequence, EventTurnEnd, EventProtected, turn, "", "", map[string]any{"stop_reason": assistant.StopReason})
			finalCopy := assistant.Clone()
			final = &finalCopy
			result := RunResult{RunID: runID, Status: RunCompleted, FinalMessage: final, Usage: totalUsage}
			a.emit(runID, &sequence, EventAgentEnd, EventProtected, turn, "", "", map[string]any{"status": result.Status})
			return result, nil
		}

		for sourceIndex, call := range assistant.ToolCalls {
			toolCalls++
			if limitExceededUint32(a.limitsSet, a.limits.MaxToolCalls, toolCalls) {
				return fail(runtimeError(ErrLimitExceeded, "Tool", "maximum Tool Calls exceeded", nil))
			}
			tool := a.tools[call.ToolID]
			if tool == nil {
				return fail(runtimeError(ErrToolResolutionFailure, "Tool", "unknown Tool: "+call.ToolID, nil))
			}
			if !json.Valid(call.Arguments) {
				return fail(runtimeError(ErrToolArgumentFailure, "Tool", "Tool arguments are not valid JSON", nil))
			}
			if err := validateToolSchema(tool.Spec().InputSchema, call.Arguments); err != nil {
				return fail(runtimeError(ErrToolArgumentFailure, "Tool", err.Error(), err))
			}

			use := ToolUse{RunID: runID, Turn: turn, CallID: call.ID, QualifiedID: call.ToolID, ArgumentsJSON: slices.Clone(call.Arguments), SourceIndex: uint32(sourceIndex)}
			a.emit(runID, &sequence, EventToolExecutionStart, EventProtected, turn, assistant.ID, call.ID, map[string]any{"tool_id": call.ToolID})
			toolCtx, toolCancel := boundedContext(runCtx, a.limits.ToolCallDeadline)
			var progressBytes uint64
			var progressUpdates uint32
			progress := func(text string) {
				if (a.limitsSet && a.limits.MaxToolProgressUpdates == 0) || (a.limits.MaxToolProgressUpdates > 0 && progressUpdates >= a.limits.MaxToolProgressUpdates) {
					return
				}
				if (a.limitsSet && a.limits.MaxToolProgressBytes == 0) || (a.limits.MaxToolProgressBytes > 0 && progressBytes+uint64(len(text)) > a.limits.MaxToolProgressBytes) {
					return
				}
				progressUpdates++
				progressBytes += uint64(len(text))
				a.emit(runID, &sequence, EventToolExecutionUpdate, EventCoalescable, turn, assistant.ID, call.ID, map[string]any{"text": text})
			}
			toolResult, toolErr := executeToolSafely(tool, toolCtx, use, progress)
			toolCancel()
			use.Executed = true
			if toolErr != nil {
				toolResult = ToolResult{CallID: call.ID, Status: ToolResultFailed, SafeError: safeError(toolErr), Executed: true}
			} else {
				toolResult.CallID = call.ID
				toolResult.Executed = true
				if toolResult.Status == "" {
					toolResult.Status = ToolResultOK
				}
			}
			if runCtx.Err() != nil {
				toolResult.Status = ToolResultCanceled
				if toolResult.SafeError == "" {
					toolResult.SafeError = safeError(runCtx.Err())
				}
			}
			if err := validateToolResultSize(a.limits, a.limitsSet, toolResult); err != nil {
				return fail(err)
			}
			use.Result = &toolResult
			content := cloneContent(toolResult.Content)
			resultMessage := Message{ID: MessageID(nextID("message")), Role: RoleToolResult, Parts: content, ToolResult: ptrToolResult(toolResult)}
			if err := a.commitMessage(resultMessage); err != nil {
				return fail(asRuntimeError(err))
			}
			a.emit(runID, &sequence, EventToolExecutionEnd, EventProtected, turn, assistant.ID, call.ID, map[string]any{"status": toolResult.Status, "executed": toolResult.Executed})
			a.emit(runID, &sequence, EventToolResultCommitted, EventProtected, turn, resultMessage.ID, call.ID, map[string]any{"status": toolResult.Status})
		}
		a.emit(runID, &sequence, EventTurnEnd, EventProtected, turn, "", "", map[string]any{"stop_reason": StopToolCalls})
		if err := contextFailure(runCtx, "continuation"); err != nil {
			return fail(err)
		}
	}
}

func (a *coreAgent) readAssistant(ctx context.Context, runID RunID, sequence *uint64, turn TurnNumber, request ModelRequest) (Message, Usage, *RuntimeError) {
	modelCtx, cancel := boundedContext(ctx, a.limits.ModelCallDeadline)
	defer cancel()
	stream, err := a.model.Stream(modelCtx, request)
	if err != nil {
		return Message{}, Usage{}, classifyModelError(err)
	}
	if stream == nil {
		return Message{}, Usage{}, runtimeError(ErrModelFailure, "Model.Stream", "Model returned a nil stream", nil)
	}
	defer stream.Close()

	assistant := Message{ID: MessageID(nextID("message")), Role: RoleAssistant}
	a.emit(runID, sequence, EventMessageStart, EventProtected, turn, assistant.ID, "", map[string]any{"role": string(RoleAssistant)})
	var usage Usage
	var complete bool
	for {
		modelEvent, recvErr := stream.Recv(modelCtx)
		if recvErr != nil {
			if errors.Is(recvErr, io.EOF) && complete {
				break
			}
			if errors.Is(recvErr, io.EOF) {
				return Message{}, Usage{}, runtimeError(ErrModelProtocolFailure, "ModelStream", "stream ended without completion", recvErr)
			}
			return Message{}, Usage{}, classifyModelError(recvErr)
		}
		if complete {
			return Message{}, Usage{}, runtimeError(ErrModelProtocolFailure, "ModelStream", "event arrived after completion", nil)
		}
		switch modelEvent.Kind {
		case ModelTextDelta:
			if modelEvent.Text != "" {
				assistant.Parts = append(assistant.Parts, ContentPart{Kind: ContentText, Text: modelEvent.Text})
				a.emit(runID, sequence, EventMessageUpdate, EventCoalescable, turn, assistant.ID, "", map[string]any{"text": modelEvent.Text})
			}
		case ModelReasoningDelta:
			if modelEvent.Text != "" {
				assistant.Parts = append(assistant.Parts, ContentPart{Kind: ContentReasoning, Text: modelEvent.Text})
			}
		case ModelReasoningDone:
			partIndex := -1
			for i := len(assistant.Parts) - 1; i >= 0; i-- {
				if assistant.Parts[i].Kind == ContentReasoning {
					partIndex = i
					break
				}
			}
			if partIndex == -1 {
				assistant.Parts = append(assistant.Parts, ContentPart{Kind: ContentReasoning})
				partIndex = len(assistant.Parts) - 1
			}
			assistant.Parts[partIndex].Signature = slices.Clone(modelEvent.ReasoningArtifact)
		case ModelToolCall:
			if modelEvent.ToolCall == nil || modelEvent.ToolCall.ID == "" || modelEvent.ToolCall.ToolID == "" {
				return Message{}, Usage{}, runtimeError(ErrModelProtocolFailure, "ModelStream", "invalid Tool Call", nil)
			}
			for _, existing := range assistant.ToolCalls {
				if existing.ID == modelEvent.ToolCall.ID {
					return Message{}, Usage{}, runtimeError(ErrModelProtocolFailure, "ModelStream", "duplicate Tool Call ID", nil)
				}
			}
			call := *modelEvent.ToolCall
			call.Arguments = slices.Clone(call.Arguments)
			assistant.ToolCalls = append(assistant.ToolCalls, call)
		case ModelUsage:
			usage = modelEvent.Usage
		case ModelDone:
			complete = true
			assistant.StopReason = modelEvent.StopReason
			if assistant.StopReason == "" {
				assistant.StopReason = StopEndTurn
			}
			if modelEvent.Usage.TotalTokens != 0 || modelEvent.Usage.InputTokens != 0 || modelEvent.Usage.OutputTokens != 0 {
				usage = modelEvent.Usage
			}
		default:
			return Message{}, Usage{}, runtimeError(ErrModelProtocolFailure, "ModelStream", "unknown Model event", nil)
		}
		if complete {
			break
		}
	}
	if !complete {
		return Message{}, Usage{}, runtimeError(ErrModelProtocolFailure, "ModelStream", "missing completion", nil)
	}
	return assistant, usage, nil
}

func executeToolSafely(tool Tool, ctx context.Context, use ToolUse, progress ToolProgress) (result ToolResult, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("tool panic: %v", recovered)
			_ = debug.Stack()
		}
	}()
	return tool.Execute(ctx, use, progress)
}

func boundedContext(parent context.Context, duration time.Duration) (context.Context, context.CancelFunc) {
	if duration > 0 {
		return context.WithTimeout(parent, duration)
	}
	return context.WithCancel(parent)
}

func contextFailure(ctx context.Context, operation string) *RuntimeError {
	if err := ctx.Err(); err != nil {
		return runtimeError(codeForContext(err), operation, err.Error(), err)
	}
	return nil
}

func classifyModelError(err error) *RuntimeError {
	if err == nil {
		return runtimeError(ErrModelFailure, "Model", "unknown Model failure", nil)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return runtimeError(codeForContext(err), "Model", err.Error(), err)
	}
	return runtimeError(ErrModelFailure, "Model", err.Error(), err)
}

func validateLimits(limits CoreLimits) error {
	if limits.RunDeadline < 0 || limits.ModelCallDeadline < 0 || limits.ToolCallDeadline < 0 {
		return runtimeError(ErrInvalidArgument, "CoreLimits", "deadlines cannot be negative", nil)
	}
	return nil
}

func validatePrompt(message Message) error {
	if message.Role != RoleUser {
		return runtimeError(ErrInvalidArgument, "Prompt", "Prompt message must have role user", nil)
	}
	if strings.TrimSpace(TextOf(message)) == "" && len(message.Parts) == 0 {
		return runtimeError(ErrInvalidArgument, "Prompt", "Prompt message is empty", nil)
	}
	return nil
}

func validateToolSpec(spec ToolSpec) error {
	if strings.TrimSpace(spec.ID) == "" {
		return runtimeError(ErrInvalidArgument, "ToolSpec", "Tool ID is empty", nil)
	}
	if spec.InputSchema != nil {
		var schema map[string]any
		if err := json.Unmarshal(spec.InputSchema, &schema); err != nil {
			return runtimeError(ErrInvalidArgument, "ToolSpec", "invalid InputSchema", err)
		}
		if schema == nil {
			return runtimeError(ErrInvalidArgument, "ToolSpec", "InputSchema must be an object", nil)
		}
	}
	return nil
}

func validateToolSchema(schema, arguments []byte) error {
	if len(schema) == 0 {
		return nil
	}
	var root map[string]any
	if err := json.Unmarshal(schema, &root); err != nil {
		return fmt.Errorf("invalid InputSchema: %w", err)
	}
	var value any
	if err := json.Unmarshal(arguments, &value); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return validateSchemaValue(root, value, "arguments")
}

func validateSchemaValue(schema map[string]any, value any, path string) error {
	if typ, ok := schema["type"].(string); ok && !schemaTypeMatches(typ, value) {
		return fmt.Errorf("%s must be %s", path, typ)
	}
	if required, ok := schema["required"].([]any); ok {
		object, isObject := value.(map[string]any)
		if !isObject {
			return fmt.Errorf("%s must be object", path)
		}
		for _, raw := range required {
			name, isString := raw.(string)
			if isString {
				if _, exists := object[name]; !exists {
					return fmt.Errorf("%s.%s is required", path, name)
				}
			}
		}
	}
	properties, _ := schema["properties"].(map[string]any)
	if object, ok := value.(map[string]any); ok {
		for name, rawSchema := range properties {
			if raw, exists := object[name]; exists {
				if child, ok := rawSchema.(map[string]any); ok {
					if err := validateSchemaValue(child, raw, path+"."+name); err != nil {
						return err
					}
				}
			}
		}
		if additional, ok := schema["additionalProperties"].(bool); ok && !additional {
			for name := range object {
				if _, declared := properties[name]; !declared {
					return fmt.Errorf("%s.%s is not allowed", path, name)
				}
			}
		}
	}
	return nil
}

func schemaTypeMatches(typ string, value any) bool {
	switch typ {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number", "integer":
		_, ok := value.(float64)
		return ok
	case "null":
		return value == nil
	default:
		return true
	}
}

func validateToolResultSize(limits CoreLimits, explicit bool, result ToolResult) *RuntimeError {
	bytes, err := json.Marshal(result)
	if err != nil {
		return runtimeError(ErrInternalInvariant, "ToolResult", "cannot encode Tool Result", err)
	}
	if limitExceededUint64(explicit, limits.MaxToolResultBytes, uint64(len(bytes))) {
		return runtimeError(ErrLimitExceeded, "ToolResult", "maximum Tool Result bytes exceeded", nil)
	}
	return nil
}

func limitExceededUint32(explicit bool, limit, value uint32) bool {
	return (explicit && limit == 0) || (limit > 0 && value > limit)
}

func limitExceededUint64(explicit bool, limit, value uint64) bool {
	return (explicit && limit == 0) || (limit > 0 && value > limit)
}

func safeError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func asRuntimeError(err error) *RuntimeError {
	if err == nil {
		return nil
	}
	var runtimeErr *RuntimeError
	if errors.As(err, &runtimeErr) {
		return runtimeErr
	}
	return runtimeError(ErrInternalInvariant, "Core", err.Error(), err)
}

func cloneMessages(messages []Message) []Message {
	out := make([]Message, len(messages))
	for i, message := range messages {
		out[i] = message.Clone()
	}
	return out
}

func cloneContent(content []ContentPart) []ContentPart {
	out := make([]ContentPart, len(content))
	for i, part := range content {
		out[i] = part
		out[i].Data = slices.Clone(part.Data)
		out[i].Metadata = maps.Clone(part.Metadata)
	}
	return out
}

func ptrToolResult(result ToolResult) *ToolResult { clone := result.Clone(); return &clone }

func cloneToolSpec(spec ToolSpec) ToolSpec {
	out := spec
	out.InputSchema = slices.Clone(spec.InputSchema)
	out.OutputSchema = slices.Clone(spec.OutputSchema)
	out.Metadata = maps.Clone(spec.Metadata)
	return out
}

func cloneToolSpecs(specs []ToolSpec) []ToolSpec {
	out := make([]ToolSpec, len(specs))
	for i, spec := range specs {
		out[i] = cloneToolSpec(spec)
	}
	return out
}

func boundedReason(reason string) string {
	if len(reason) > 256 {
		return reason[:256]
	}
	return reason
}
