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

// ControllableAgent adds the control operations that share the one canonical
// Loop. They are additive: the minimal Agent path stays two methods.
//
// Continue runs the Loop without appending a user Message and is valid only
// when the transcript already ends in a Model-continuable state.
//
// Steer and FollowUp deliver a bounded control Message consumed at a defined
// safe boundary. Neither interrupts the Model or Tool work in flight. Steer is
// consumed at the next Turn boundary; FollowUp is consumed only where the Run
// would otherwise settle.
//
// Abort cancels the current Run without closing the Agent.
type ControllableAgent interface {
	Agent
	Continue(context.Context) (RunResult, error)
	Steer(Message) error
	FollowUp(Message) error
	Abort()
}

type Option func(*agentConfig) error

type agentConfig struct {
	model         Model
	instruction   string
	tools         []Tool
	toolSets      []toolSetConfig
	rootNamespace string
	extensions    extensionSet
	limits        CoreLimits
	limitsSet     bool
	initial       CoreSnapshot
	hasInitial    bool
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

	registry, err := newToolRegistry(&cfg)
	if err != nil {
		return nil, err
	}

	a := &coreAgent{
		id:           AgentID(nextID("agent")),
		model:        cfg.model,
		instruction:  cfg.instruction,
		registry:     registry,
		extensions:   cfg.extensions,
		limits:       cfg.limits,
		limitsSet:    cfg.limitsSet,
		commands:     make(chan agentCommand),
		closeSignal:  make(chan struct{}),
		done:         make(chan struct{}),
		ready:        make(chan struct{}),
		admission:    make(chan struct{}, 1),
		steer:        make(chan Message, controlCapacity(cfg.limits.MaxSteerMessages)),
		followUp:     make(chan Message, controlCapacity(cfg.limits.MaxFollowUpMessages)),
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
		if err := registry.restoreActive(cfg.initial.ActiveToolSets); err != nil {
			return nil, err
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
	registry    *toolRegistry
	extensions  extensionSet
	limits      CoreLimits
	limitsSet   bool

	// observerCtx belongs to the Run in flight. Only the Agent goroutine
	// reads and writes it, so Event observers see the owning Run Context.
	observerCtx context.Context

	commands    chan agentCommand
	closeSignal chan struct{}
	closeOnce   sync.Once
	done        chan struct{}
	doneOnce    sync.Once
	ready       chan struct{}
	admission   chan struct{}
	steer       chan Message
	followUp    chan Message

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
	commandContinue
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
	if err := validatePrompt(message); err != nil {
		return RunResult{}, err
	}
	return a.submit(ctx, commandPrompt, "Prompt", message)
}

// Continue runs the Loop without appending a user Message. It is valid only
// when the committed transcript already ends in a Model-continuable state.
func (a *coreAgent) Continue(ctx context.Context) (RunResult, error) {
	return a.submit(ctx, commandContinue, "Continue", Message{})
}

func (a *coreAgent) submit(ctx context.Context, kind agentCommandKind, operation string, message Message) (RunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := a.admissionError(); err != nil {
		return RunResult{}, err
	}

	select {
	case a.admission <- struct{}{}:
	default:
		return RunResult{}, runtimeError(ErrBusy, operation, "Agent is already processing a Run", nil)
	}
	releaseAdmission := true
	defer func() {
		if releaseAdmission {
			<-a.admission
		}
	}()

	cmd := agentCommand{kind: kind, ctx: ctx, message: message, result: make(chan promptResponse, 1)}
	select {
	case a.commands <- cmd:
		releaseAdmission = false
	case <-ctx.Done():
		return RunResult{}, runtimeError(codeForContext(ctx.Err()), operation, operation+" was cancelled before admission", ctx.Err())
	case <-a.closeSignal:
		return RunResult{}, a.admissionError()
	}

	select {
	case response := <-cmd.result:
		return response.result, response.err
	case <-ctx.Done():
		return RunResult{}, runtimeError(codeForContext(ctx.Err()), operation, operation+" context ended", ctx.Err())
	}
}

// Steer delivers guidance for the Run in flight. The Agent commits it at the
// next Turn boundary; it never interrupts the current Model or Tool work. A
// Steer that arrives where the Run would settle keeps the Run going for one
// more Turn rather than being dropped.
func (a *coreAgent) Steer(message Message) error {
	return a.enqueueControl(a.steer, message, "Steer")
}

// FollowUp supplies the continuation to use where the Run would otherwise
// settle. It is not a general external request queue: the buffer is bounded
// and belongs to this Agent.
func (a *coreAgent) FollowUp(message Message) error {
	return a.enqueueControl(a.followUp, message, "FollowUp")
}

func (a *coreAgent) enqueueControl(target chan Message, message Message, operation string) error {
	if err := validateControlMessage(message, operation); err != nil {
		return err
	}
	if err := a.admissionError(); err != nil {
		return err
	}
	if cap(target) == 0 {
		return runtimeError(ErrLimitExceeded, operation, operation+" is disabled by the configured limit", nil)
	}
	select {
	case target <- message.Clone():
		return nil
	default:
		return runtimeError(ErrLimitExceeded, operation, operation+" buffer is full", nil)
	}
}

// Abort cancels the current Run. It does not close the Agent, and it is a
// no-op when no Run is active.
func (a *coreAgent) Abort() {
	a.runMu.Lock()
	stop := a.currentRunStop
	a.runMu.Unlock()
	if stop != nil {
		stop()
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
			case commandPrompt, commandContinue:
				if err := a.admissionError(); err != nil {
					cmd.result <- promptResponse{err: err}
					<-a.admission
					continue
				}
				if cmd.kind == commandContinue {
					if err := validateContinuable(a.messages); err != nil {
						cmd.result <- promptResponse{err: err}
						<-a.admission
						continue
					}
				}
				a.setStatus(AgentBusy)
				var prompt *Message
				if cmd.kind == commandPrompt {
					prompt = &cmd.message
				}
				result, err := a.executeRun(cmd.ctx, prompt)
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
		ActiveToolSets:     a.registry.activeNames(),
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

// emit assigns the next Run sequence number, awaits local Event observers, and
// publishes the Event. A blocking observer failure settles the owning Run, so
// the caller must not continue past a non-nil result.
func (a *coreAgent) emit(runID RunID, seq *uint64, kind EventKind, class EventClass, turn TurnNumber, messageID MessageID, callID ToolCallID, payload map[string]any) *RuntimeError {
	*seq++
	event := Event{AgentID: a.id, RunID: runID, Sequence: *seq, Kind: kind, Class: class, Turn: turn, MessageID: messageID, ToolCallID: callID, Payload: payload, Timestamp: time.Now()}
	var failure *RuntimeError
	if len(a.extensions.observers) > 0 {
		ctx := a.observerCtx
		if ctx == nil {
			ctx = context.Background()
		}
		failure = a.extensions.observe(ctx, event)
	}
	a.events.publish(event)
	return failure
}

func addUsage(total, current Usage) Usage {
	total.InputTokens += current.InputTokens
	total.OutputTokens += current.OutputTokens
	total.TotalTokens += current.TotalTokens
	return total
}

// summarizeTurn builds a bounded, provider-neutral heartbeat payload. It
// describes work performed by the loop without copying prompt, answer,
// reasoning, or Tool arguments into operational logs.
func summarizeTurn(assistant Message, usage Usage, elapsed time.Duration, toolResults []map[string]any) map[string]any {
	var textBytes, reasoningBytes uint64
	for _, part := range assistant.Parts {
		switch part.Kind {
		case ContentText:
			textBytes += uint64(len(part.Text))
		case ContentReasoning:
			reasoningBytes += uint64(len(part.Text))
		}
	}
	summary := map[string]any{
		"elapsed_ms":      elapsed.Milliseconds(),
		"text_bytes":      textBytes,
		"reasoning_bytes": reasoningBytes,
		"tool_calls":      len(assistant.ToolCalls),
		"input_tokens":    usage.InputTokens,
		"output_tokens":   usage.OutputTokens,
		"total_tokens":    usage.TotalTokens,
	}
	if len(toolResults) > 0 {
		summary["tool_results"] = toolResults
	}
	return summary
}

// executeRun runs the one canonical Loop. A nil prompt is a Continue: the Run
// appends no user Message and picks up the committed transcript as it stands.
func (a *coreAgent) executeRun(ctx context.Context, prompt *Message) (RunResult, error) {
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
	var totalUsage Usage
	var final *Message
	var toolCalls uint32
	var turns uint32
	var textBytes, reasoningBytes uint64
	runStarted := time.Now()
	runMetrics := func() RunMetrics {
		return RunMetrics{
			ElapsedMS:      time.Since(runStarted).Milliseconds(),
			Turns:          turns,
			ToolCalls:      toolCalls,
			TextBytes:      textBytes,
			ReasoningBytes: reasoningBytes,
		}
	}
	a.observerCtx = runCtx
	defer func() { a.observerCtx = nil }()

	fail := func(err *RuntimeError) (RunResult, error) {
		if err == nil {
			err = runtimeError(ErrInternalInvariant, "Run", "nil terminal error", nil)
		}
		result := RunResult{RunID: runID, Status: RunFailed, Usage: totalUsage, Metrics: runMetrics(), Error: err}
		if errors.Is(err.Cause, context.Canceled) || err.Code == ErrCancelled {
			result.Status = RunCanceled
		}
		if err.Code == ErrDeadlineExceeded {
			result.Status = RunDeadlineExceeded
		}
		// The terminal Event is emitted even when an observer already failed:
		// a Run may not end without exactly one agent_end.
		_ = a.emit(runID, &sequence, EventAgentEnd, EventProtected, 0, "", "", map[string]any{"status": result.Status, "error": err.Code})
		a.discardControl()
		return result, err
	}

	if err := a.emit(runID, &sequence, EventAgentStart, EventProtected, 0, "", "", nil); err != nil {
		return fail(err)
	}

	if prompt != nil {
		prompt.ID = MessageID(nextID("message"))
		if err := a.commitMessage(*prompt); err != nil {
			return fail(asRuntimeError(err))
		}
		payload := map[string]any{"role": string(prompt.Role)}
		if err := a.emit(runID, &sequence, EventMessageStart, EventProtected, 0, prompt.ID, "", payload); err != nil {
			return fail(err)
		}
		if err := a.emit(runID, &sequence, EventMessageEnd, EventProtected, 0, prompt.ID, "", payload); err != nil {
			return fail(err)
		}
	}

	for turn := TurnNumber(1); ; turn++ {
		if limitExceededUint32(a.limitsSet, a.limits.MaxTurns, uint32(turn)) {
			return fail(runtimeError(ErrLimitExceeded, "Run", "maximum Turns exceeded", nil))
		}
		if err := contextFailure(runCtx, "turn"); err != nil {
			return fail(err)
		}
		turns++
		if err := a.emit(runID, &sequence, EventTurnStart, EventProtected, turn, "", "", nil); err != nil {
			return fail(err)
		}
		turnStarted := time.Now()

		// Transformers and converters shape what this Turn sends to the
		// Model. Their output is never committed as the Core transcript.
		outbound := cloneMessages(a.messages)
		if !a.extensions.empty() {
			transformed, extensionErr := a.extensions.transformContext(runCtx, ContextSnapshot{
				AgentID:            a.id,
				RunID:              runID,
				Turn:               turn,
				SystemInstructions: a.instruction,
				Messages:           outbound,
			})
			if extensionErr != nil {
				return fail(extensionErr)
			}
			outbound = transformed
		}
		request := ModelRequest{SystemInstructions: a.instruction, Messages: outbound, Tools: a.registry.visibleSpecs()}
		assistant, usage, modelErr := a.readAssistant(runCtx, runID, &sequence, turn, request)
		totalUsage = addUsage(totalUsage, usage)
		for _, part := range assistant.Parts {
			switch part.Kind {
			case ContentText:
				textBytes += uint64(len(part.Text))
			case ContentReasoning:
				reasoningBytes += uint64(len(part.Text))
			}
		}
		if modelErr != nil {
			return fail(modelErr)
		}
		if len(assistant.ToolCalls) > 0 {
			assistant.StopReason = StopToolCalls
		}
		if err := a.commitMessage(assistant); err != nil {
			return fail(asRuntimeError(err))
		}
		if err := a.emit(runID, &sequence, EventMessageEnd, EventProtected, turn, assistant.ID, "", map[string]any{"tool_calls": len(assistant.ToolCalls)}); err != nil {
			return fail(err)
		}

		settle := func(stopped bool, reason string) (RunResult, error) {
			finalCopy := assistant.Clone()
			final = &finalCopy
			result := RunResult{RunID: runID, Status: RunCompleted, FinalMessage: final, Usage: totalUsage, Metrics: runMetrics()}
			payload := map[string]any{"status": result.Status}
			if stopped {
				payload["stopped_by_extension"] = true
				if reason != "" {
					payload["stop_reason"] = reason
				}
			}
			_ = a.emit(runID, &sequence, EventAgentEnd, EventProtected, turn, "", "", payload)
			a.discardControl()
			return result, nil
		}

		if len(assistant.ToolCalls) == 0 {
			if err := a.emit(runID, &sequence, EventTurnEnd, EventProtected, turn, "", "", map[string]any{
				"stop_reason": assistant.StopReason,
				"summary":     summarizeTurn(assistant, usage, time.Since(turnStarted), nil),
			}); err != nil {
				return fail(err)
			}
			// A TurnStopper runs after turn_end and before continuation
			// selection. A stop preserves the Turn and settles the Run.
			decision, stopErr := a.extensions.stopTurn(runCtx, TurnSnapshot{
				AgentID: a.id, RunID: runID, Turn: turn, Assistant: assistant.Clone(), StopReason: assistant.StopReason,
			})
			if stopErr != nil {
				return fail(stopErr)
			}
			if decision.Stop {
				return settle(true, decision.Reason)
			}
			// Safe boundary at settlement: Steer and FollowUp both apply here.
			// Cancellation is checked first so a queued continuation is never
			// committed to a Run that is already ending.
			if err := contextFailure(runCtx, "continuation"); err != nil {
				return fail(err)
			}
			injected, controlErr := a.consumeControl(runID, &sequence, turn, true)
			if controlErr != nil {
				return fail(controlErr)
			}
			if injected {
				continue
			}
			return settle(false, "")
		}

		// Preflight is source ordered: resolve, validate, and run the Pre
		// chain before any executor starts.
		plans, planErr := a.preflightTools(runCtx, runID, turn, assistant.ToolCalls, &toolCalls)
		if planErr != nil {
			return fail(planErr)
		}
		// A blocked Tool is already settled at preflight, so its start and end
		// Events are emitted together, in source order.
		outcomes := make(map[int]ToolResult, len(plans))
		for i := range plans {
			if err := a.emit(runID, &sequence, EventToolExecutionStart, EventProtected, turn, assistant.ID, plans[i].call.ID, map[string]any{"tool_id": plans[i].call.ToolID}); err != nil {
				return fail(err)
			}
			if plans[i].blocked {
				outcomes[i] = plans[i].result
				if err := a.emit(runID, &sequence, EventToolExecutionEnd, EventProtected, turn, assistant.ID, plans[i].call.ID, map[string]any{"status": plans[i].result.Status, "executed": plans[i].result.Executed}); err != nil {
					return fail(err)
				}
			}
		}

		// Execution may be sequential or bounded parallel. Completion Events
		// are emitted inside the group as each outcome arrives, so they follow
		// actual completion order; commitment below stays source ordered.
		for _, group := range groupPlans(plans, a.parallelWorkers()) {
			completed, groupErr := a.executeToolGroup(runCtx, runID, &sequence, turn, assistant.ID, plans, group)
			if groupErr != nil {
				return fail(groupErr)
			}
			for index, result := range completed {
				outcomes[index] = result
			}
		}

		toolResults := make([]map[string]any, 0, len(plans))
		settledResults := make([]ToolResult, 0, len(plans))
		for i := range plans {
			call := plans[i].call
			toolResult := outcomes[i]
			// Post-Tool-Use sees executed, blocked, failed, and cancelled
			// outcomes alike, in reverse installation order.
			finalized, postErr := a.extensions.afterTool(runCtx, toolResult)
			if postErr != nil {
				return fail(postErr)
			}
			toolResult = finalized
			if err := validateToolResultSize(a.limits, a.limitsSet, toolResult); err != nil {
				return fail(err)
			}
			content := cloneContent(toolResult.Content)
			resultMessage := Message{ID: MessageID(nextID("message")), Role: RoleToolResult, Parts: content, ToolResult: ptrToolResult(toolResult)}
			if err := a.commitMessage(resultMessage); err != nil {
				return fail(asRuntimeError(err))
			}
			if err := a.emit(runID, &sequence, EventToolResultCommitted, EventProtected, turn, resultMessage.ID, call.ID, map[string]any{"status": toolResult.Status}); err != nil {
				return fail(err)
			}
			settledResults = append(settledResults, toolResult.Clone())
			toolResults = append(toolResults, map[string]any{
				"tool_id":  call.ToolID,
				"call_id":  call.ID,
				"status":   toolResult.Status,
				"executed": toolResult.Executed,
			})
		}

		// ToolSet activation commits at the batch boundary, so newly active
		// Tools appear only in the next Model request.
		activated, activationErr := a.registry.commitPending()
		if activationErr != nil {
			return fail(asRuntimeError(activationErr))
		}
		for _, name := range activated {
			if err := a.emit(runID, &sequence, EventToolSetActivated, EventProtected, turn, "", "", map[string]any{"toolset": name}); err != nil {
				return fail(err)
			}
		}

		if err := a.emit(runID, &sequence, EventTurnEnd, EventProtected, turn, "", "", map[string]any{
			"stop_reason": StopToolCalls,
			"summary":     summarizeTurn(assistant, usage, time.Since(turnStarted), toolResults),
		}); err != nil {
			return fail(err)
		}
		decision, stopErr := a.extensions.stopTurn(runCtx, TurnSnapshot{
			AgentID: a.id, RunID: runID, Turn: turn, Assistant: assistant.Clone(), ToolResults: settledResults, StopReason: StopToolCalls,
		})
		if stopErr != nil {
			return fail(stopErr)
		}
		if decision.Stop {
			return settle(true, decision.Reason)
		}
		// Safe boundary between Turns: Steer applies, FollowUp waits for
		// settlement.
		if err := contextFailure(runCtx, "continuation"); err != nil {
			return fail(err)
		}
		if _, controlErr := a.consumeControl(runID, &sequence, turn, false); controlErr != nil {
			return fail(controlErr)
		}
	}
}

// consumeControl commits the control Messages that apply at this boundary. It
// runs inside the Agent execution unit, so committing them keeps the Agent
// goroutine as the only mutation authority for the transcript.
func (a *coreAgent) consumeControl(runID RunID, sequence *uint64, turn TurnNumber, settling bool) (bool, *RuntimeError) {
	injected := false
	commit := func(message Message, source string) *RuntimeError {
		message.ID = MessageID(nextID("message"))
		if err := a.commitMessage(message); err != nil {
			return asRuntimeError(err)
		}
		payload := map[string]any{"role": string(message.Role), "source": source}
		if err := a.emit(runID, sequence, EventMessageStart, EventProtected, turn, message.ID, "", payload); err != nil {
			return err
		}
		if err := a.emit(runID, sequence, EventMessageEnd, EventProtected, turn, message.ID, "", payload); err != nil {
			return err
		}
		injected = true
		return nil
	}
	for {
		select {
		case message := <-a.steer:
			if err := commit(message, "steer"); err != nil {
				return injected, err
			}
			continue
		default:
		}
		break
	}
	if !settling {
		return injected, nil
	}
	for {
		select {
		case message := <-a.followUp:
			if err := commit(message, "follow_up"); err != nil {
				return injected, err
			}
			continue
		default:
		}
		break
	}
	return injected, nil
}

// discardControl drops control Messages left over when a Run reaches its
// terminal Event. Nothing may continue after agent_end, so a leftover control
// Message must not silently reappear inside the next Run.
func (a *coreAgent) discardControl() {
	for {
		select {
		case <-a.steer:
		case <-a.followUp:
		default:
			return
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
	if err := a.emit(runID, sequence, EventMessageStart, EventProtected, turn, assistant.ID, "", map[string]any{"role": string(RoleAssistant)}); err != nil {
		return Message{}, Usage{}, err
	}
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
				if err := a.emit(runID, sequence, EventMessageUpdate, EventCoalescable, turn, assistant.ID, "", map[string]any{"text": modelEvent.Text}); err != nil {
					return Message{}, Usage{}, err
				}
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

func validateControlMessage(message Message, operation string) error {
	if message.Role != RoleUser {
		return runtimeError(ErrInvalidArgument, operation, operation+" message must have role user", nil)
	}
	if strings.TrimSpace(TextOf(message)) == "" && len(message.Parts) == 0 {
		return runtimeError(ErrInvalidArgument, operation, operation+" message is empty", nil)
	}
	return nil
}

// validateContinuable enforces that Continue never synthesizes user input: the
// transcript must already end where a Model can pick up.
func validateContinuable(messages []Message) error {
	if len(messages) == 0 {
		return runtimeError(ErrInvalidState, "Continue", "transcript is empty", nil)
	}
	switch messages[len(messages)-1].Role {
	case RoleUser, RoleToolResult:
		return nil
	default:
		return runtimeError(ErrInvalidState, "Continue", "transcript does not end in a Model-continuable state", nil)
	}
}

// controlCapacity turns a configured control-message limit into a channel
// capacity. A zero limit disables that control command.
func controlCapacity(limit uint32) int {
	if limit > maxControlCapacity {
		return maxControlCapacity
	}
	return int(limit)
}

const maxControlCapacity = 1024

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

// validateToolSchema checks Tool arguments against the supported JSON Schema
// subset. Core validates a JSON value rather than a Go struct, so `omitempty`
// on a Go field never changes what `required` means here.
//
// Enforced:
//
//	type                  object, array, string, boolean, number, integer, null
//	properties            recursive validation of declared members
//	required              declared member must be present
//	additionalProperties  false rejects undeclared members
//
// Carried to the Model but not enforced by Core: description, enum, format,
// items, and every other keyword. An unknown keyword is ignored rather than
// rejected, so a richer provider Schema still passes through unchanged.
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
	if allowed, ok := schema["enum"].([]any); ok && len(allowed) > 0 {
		if !enumAllows(allowed, value) {
			// The allowed values come from the declared Schema, so naming
			// them is safe. The received value is provider payload and stays
			// out of the message.
			return fmt.Errorf("%s must be one of: %s", path, describeEnum(allowed))
		}
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

// enumAllows compares a decoded JSON value against the declared enum. Both
// sides come from encoding/json, so the dynamic types are limited to string,
// float64, bool, and nil.
func enumAllows(allowed []any, value any) bool {
	for _, candidate := range allowed {
		switch expected := candidate.(type) {
		case string:
			if actual, ok := value.(string); ok && actual == expected {
				return true
			}
		case float64:
			if actual, ok := value.(float64); ok && actual == expected {
				return true
			}
		case bool:
			if actual, ok := value.(bool); ok && actual == expected {
				return true
			}
		case nil:
			if value == nil {
				return true
			}
		}
	}
	return false
}

func describeEnum(allowed []any) string {
	names := make([]string, 0, len(allowed))
	for _, candidate := range allowed {
		names = append(names, fmt.Sprintf("%v", candidate))
	}
	return strings.Join(names, ", ")
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
