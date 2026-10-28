package gotato

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// SessionStatus is where a live Session is in its life: created, idle while
// it waits for a Run, busy during one, then closing and closed.
type SessionStatus string

// The statuses of a live Session. A Session starts created, is idle between
// Runs and busy during one, and ends closing, then closed.
const (
	SessionStatusCreated SessionStatus = "created"
	SessionStatusIdle    SessionStatus = "idle"
	SessionStatusBusy    SessionStatus = "busy"
	SessionStatusClosing SessionStatus = "closing"
	SessionStatusClosed  SessionStatus = "closed"
)

// SessionOption configures a Session built by NewSession.
type SessionOption func(*sessionConfig) error

type sessionConfig struct {
	model          Model
	instruction    string
	tools          []Tool
	toolSets       []toolSetConfig
	toolSources    []ToolSource
	toolFilter     func(string) bool
	toolSearcher   ToolSearcher
	extensions     extensionSet
	limits         CoreLimits
	transcript     Transcript
	contextBuilder ContextBuilder
	modelConfig    ModelConfig
	cacheKey       string
	maxEffect      ToolEffect
	feed           *Feed
	onRunStart     func(RunID)
	handoff        bool
}

// WithFeed has the Session publish its Events to feed instead of a Feed of
// its own, so the host that built it, such as a service.Runner, can offer
// the Session's stream across Runs. The host owns the Feed and closes it; it
// also sets the Feed's source, which is where a subscriber that falls behind
// catches up.
func WithFeed(feed *Feed) SessionOption {
	return func(c *sessionConfig) error {
		if feed == nil {
			return runtimeError(CodeInvalidArgument, "WithFeed", "feed is nil", nil)
		}
		c.feed = feed
		return nil
	}
}

// WithRunStart calls fn on the Session's goroutine with each Run's ID, as the
// Run starts and before its first Event. fn must not block. A host uses it
// to find the Run it started.
func WithRunStart(fn func(RunID)) SessionOption {
	return func(c *sessionConfig) error { c.onRunStart = fn; return nil }
}

// WithModel sets the Model the Session calls. NewSession fails when none is
// set.
func WithModel(model Model) SessionOption {
	return func(c *sessionConfig) error {
		if model == nil {
			return runtimeError(CodeInvalidArgument, "WithModel", "model is nil", nil)
		}
		c.model = model
		return nil
	}
}

// WithInstruction sets the system instructions the Session sends with every
// model call, before the ContextBuilder sees them.
func WithInstruction(instruction string) SessionOption {
	return func(c *sessionConfig) error { c.instruction = instruction; return nil }
}

// WithModelConfig sets the sampling and reasoning hints the Session sends on
// every model call. Provider adapters map the fields they support.
func WithModelConfig(options ModelConfig) SessionOption {
	return func(c *sessionConfig) error {
		if options.Temperature != nil && *options.Temperature < 0 {
			return runtimeError(CodeInvalidArgument, "WithModelConfig", "temperature is negative", nil)
		}
		if options.Temperature != nil {
			temperature := *options.Temperature
			options.Temperature = &temperature
		}
		c.modelConfig = options
		return nil
	}
}

// WithCacheKey sets the ModelRequest.CacheKey the Session sends on every model
// call: a stable value for one conversation, such as a hash of its Session ID,
// so the provider serves its Turns from one prompt cache. It never changes the
// prompt.
func WithCacheKey(key string) SessionOption {
	return func(c *sessionConfig) error { c.cacheKey = key; return nil }
}

// WithMaxEffect caps the Effect of the Tools the Session offers. A Tool above
// the cap, including one in a ToolSet, is never in the catalog, never found by
// a search, never loaded and never shown to the Model, and a call to it fails
// as a call to an unknown Tool. A Tool that declares no Effect ranks as
// ToolEffectDestructive, so any lower cap hides it. ToolEffectUnspecified means
// no cap, which is the default.
func WithMaxEffect(effect ToolEffect) SessionOption {
	return func(c *sessionConfig) error {
		if !effect.Valid() {
			return runtimeError(CodeInvalidArgument, "WithMaxEffect", "unknown Effect "+string(effect), nil)
		}
		c.maxEffect = effect
		return nil
	}
}

// WithTools adds Tools the Model can call from the first Turn. It fails with
// CodeInvalidArgument when a Tool is nil. Tools from a ToolSet or a ToolSource
// are added by their own options.
func WithTools(tools ...Tool) SessionOption {
	return func(c *sessionConfig) error {
		for _, tool := range tools {
			if tool == nil {
				return runtimeError(CodeInvalidArgument, "WithTools", "tool is nil", nil)
			}
		}
		c.tools = append(c.tools, tools...)
		return nil
	}
}

// WithLimits sets the Session's CoreLimits; a zero field keeps its default. It
// fails with CodeInvalidArgument when a deadline is negative.
func WithLimits(limits CoreLimits) SessionOption {
	return func(c *sessionConfig) error {
		if limits.ModelCallDeadline < 0 || limits.ToolCallDeadline < 0 {
			return runtimeError(CodeInvalidArgument, "WithLimits", "deadlines cannot be negative", nil)
		}
		c.limits = limits
		return nil
	}
}

// NewSession builds a live Session from an Agent's parts, given as options,
// and starts it. A Model is required. The Session takes its ID from its
// Transcript when the Transcript has an ID, as a session.Record does, and
// generates one otherwise. Close it when it is no longer needed.
//
// NewSession cannot be canceled: it resolves ToolSets with a background
// context. Prefer NewSessionContext.
func NewSession(options ...SessionOption) (*Session, error) {
	return NewSessionContext(context.Background(), options...)
}

// NewSessionContext is NewSession with a context for the work it does before
// the Session exists: resolving the ToolSets installed active and the ones
// the Session's earlier Runs activated. When ctx ends first, it fails with
// canceled or deadline_exceeded and builds nothing. ctx does not outlive the
// call; the Session's own Runs take their contexts as before.
func NewSessionContext(ctx context.Context, options ...SessionOption) (*Session, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, runtimeError(codeForContext(err), "NewSession", "context ended", err)
	}
	cfg := sessionConfig{}
	for _, option := range options {
		if option == nil {
			continue
		}
		if err := option(&cfg); err != nil {
			return nil, err
		}
	}
	if cfg.model == nil {
		return nil, runtimeError(CodeInvalidArgument, "NewSession", "a Model is required", nil)
	}
	if err := validateLimits(cfg.limits); err != nil {
		return nil, err
	}
	cfg.limits = cfg.limits.withDefaults()

	registry, err := newToolRegistry(ctx, &cfg)
	if err != nil {
		return nil, err
	}
	if cfg.transcript == nil {
		cfg.transcript = &memoryTranscript{}
	}
	if cfg.contextBuilder == nil {
		cfg.contextBuilder = FullHistoryContext()
	}
	id := SessionID(nextID("session"))
	if record, ok := cfg.transcript.(interface{ ID() string }); ok && record.ID() != "" {
		id = SessionID(record.ID())
	}

	a := &Session{
		id:             id,
		model:          cfg.model,
		instruction:    cfg.instruction,
		registry:       registry,
		transcript:     cfg.transcript,
		contextBuilder: cfg.contextBuilder,
		extensions:     cfg.extensions,
		limits:         cfg.limits,
		modelConfig:    cfg.modelConfig,
		cacheKey:       cfg.cacheKey,
		commands:       make(chan sessionCommand),
		closeSignal:    make(chan struct{}),
		done:           make(chan struct{}),
		ready:          make(chan struct{}),
		admission:      make(chan struct{}, 1),
		steer:          make(chan control, controlCapacity(cfg.limits.MaxSteerMessages)),
		followUp:       make(chan control, controlCapacity(cfg.limits.MaxFollowUpMessages)),
		noticeLimit:    int(min(cfg.limits.MaxNotices, maxControlCapacity)),
		stateChange:    make(chan struct{}),
		onRunStart:     cfg.onRunStart,
		handoff:        cfg.handoff,
	}
	if journal, ok := cfg.transcript.(Journal); ok {
		a.journal = journal
		a.durable = true
	} else {
		// A Transcript that keeps no Events: they are numbered in memory,
		// after the last number of a Feed the host shares with other Runs.
		journal := &memoryJournal{}
		if cfg.feed != nil {
			journal.last = cfg.feed.Last()
		}
		a.journal = journal
	}
	a.feed = cfg.feed
	if a.feed == nil {
		a.feed = NewFeed(nil)
		a.ownFeed = true
		if source, ok := cfg.transcript.(EventSource); ok {
			a.feed.setSource(source)
		}
	}
	a.extensions.report = a.reportExtension
	a.abortCond = sync.NewCond(&a.abortMu)
	a.setStatus(SessionStatusCreated)
	go a.loop()
	<-a.ready
	return a, nil
}

// Session is a live Session: one conversation running one Agent. Prompt,
// Continue, Steer, FollowUp and Abort drive its Loop; Subscribe and
// SubscribeRun stream its Events; Status and Done report its life; Tools
// and InspectContext show what its next Turn would send.
//
// The Runner builds one from a session.Record for each Run and closes it
// afterwards; between Runs it keeps the Record and its Writer warm in memory
// (service.WarmPolicy). The Record stays the truth.
type Session struct {
	id          SessionID
	model       Model
	instruction string
	registry    *toolRegistry
	registryMu  sync.Mutex
	extensions  extensionSet
	limits      CoreLimits
	// modelConfig and cacheKey are sent unchanged with every model call.
	modelConfig ModelConfig
	cacheKey    string

	// transcript is the committed history, such as a session.Record. The Session
	// goroutine is its only writer during a Run. transcriptBytes tracks the
	// encoded size incrementally so a commit never re-encodes the history.
	transcript      Transcript
	transcriptBytes uint64
	contextBuilder  ContextBuilder
	// journal keeps the Session's stored Events: the Transcript when it is a
	// Journal (durable), and a numbering of its own otherwise.
	journal Journal
	durable bool
	feed    *Feed
	ownFeed bool
	// staged holds the stored Events of the step in progress, on the Session
	// goroutine. They are written, and then sent, by flush. emitMu makes a
	// write and its sending one step, so Events reach subscribers in the
	// order of their numbers, whichever goroutine writes them.
	staged     []Event
	emitMu     sync.Mutex
	lastNumber atomic.Uint64
	onRunStart func(RunID)
	// handoff keeps the messages still queued when a Run ends in a Run that
	// completed or failed, for the host to take (TakeQueued).
	handoff bool
	ctlSeq  atomic.Uint64

	// aborts counts the AbortRequested writes in flight, so a Run's RunEnded
	// is written after them.
	abortMu   sync.Mutex
	abortCond *sync.Cond
	aborts    int

	commands    chan sessionCommand
	closeSignal chan struct{}
	closeOnce   sync.Once
	done        chan struct{}
	doneOnce    sync.Once
	ready       chan struct{}
	admission   chan struct{}
	steer       chan control
	followUp    chan control
	// notices waits for the next model call, in the order it arrived; it is
	// part of the queue the Steer is in (G-F29). noticeMu guards it.
	noticeMu    sync.Mutex
	notices     []control
	noticeLimit int

	// intrMu guards the Interrupts that wait in steer (intrPending), and the
	// cancellation of the model call in flight (callCancel).
	intrMu      sync.Mutex
	intrPending int
	callCancel  context.CancelFunc

	closeRequested atomic.Bool
	inRun          atomic.Bool
	status         atomic.Uint32

	stateMu     sync.Mutex
	stateChange chan struct{}

	runMu          sync.Mutex
	currentRunID   RunID
	currentTurn    TurnNumber
	runEnding      bool
	currentRunStop context.CancelFunc
	// endingHook, for tests, runs when a Run has begun to write its end.
	endingHook func()
}

type sessionCommandKind uint8

const (
	commandPrompt sessionCommandKind = iota + 1
	commandContinue
)

type sessionCommand struct {
	kind    sessionCommandKind
	ctx     context.Context
	message Message
	result  chan promptResponse
}

type promptResponse struct {
	result RunResult
	err    error
}

// ID is the Session's ID.
func (a *Session) ID() SessionID { return a.id }

// Status is where the Session is in its life. It reports
// SessionStatusClosing as soon as Close is requested, until the Session is
// closed.
func (a *Session) Status() SessionStatus {
	status := decodeSessionStatus(a.status.Load())
	if a.closeRequested.Load() && status != SessionStatusClosed {
		return SessionStatusClosing
	}
	return status
}

// Done returns a channel that is closed once the Session has closed: after
// Close, when no Run is left and the Session has stopped accepting messages.
func (a *Session) Done() <-chan struct{} { return a.done }

// AbortRun stops the Run named runID. It is not a no-op for a Run that is
// not in flight: it returns CodeInvalidState when runID is not the current
// Run, or when that Run is already writing its end, and CodeInvalidArgument
// when runID is empty. A canceled ctx returns its error and aborts nothing.
func (a *Session) AbortRun(ctx context.Context, runID RunID) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if runID == "" {
		return runtimeError(CodeInvalidArgument, "AbortRun", "Run ID is required", nil)
	}
	a.runMu.Lock()
	currentID := a.currentRunID
	a.runMu.Unlock()
	if currentID != runID || !a.abort() {
		return runtimeError(CodeInvalidState, "AbortRun", "Run is not active", nil)
	}
	return nil
}

// Subscribe starts a Subscription to the Session's stored Events and deltas,
// from now on, across Runs. Subscribe before the first Prompt to see
// everything. A subscriber that reads slowly slows only itself: the Session
// never waits for it.
func (a *Session) Subscribe(ctx context.Context, options ...SubscribeOption) (Subscription, error) {
	return a.feed.Subscribe(ctx, options...)
}

// Published returns how many Events the Session has published: its stored
// Events and its deltas. A test that reads a Subscription compares it with how
// many it has read, to know that it has seen everything.
func (a *Session) Published() uint64 { return a.feed.Published() }

// SubscribeRun starts a Subscription to the next Run that starts: its Events
// and deltas, from its RunStarted to its RunEnded, with the same numbers the
// Session's stream gives them. Call it before Prompt.
func (a *Session) SubscribeRun(ctx context.Context) (Subscription, error) {
	return a.feed.Subscribe(ctx, SubscribeNextRun()...)
}

// Prompt appends message as the user's Message and runs it to a settled
// RunResult. It blocks until the Run ends. It returns CodeBusy at once when a
// Run is already in flight, and an error of CodeSessionClosing or
// CodeSessionClosed after Close. Canceling ctx aborts the Run.
func (a *Session) Prompt(ctx context.Context, message Message) (RunResult, error) {
	if err := validatePrompt(message); err != nil {
		return RunResult{}, err
	}
	return a.submit(ctx, commandPrompt, "Prompt", message)
}

// Continue runs the Loop without appending a user Message. It is valid only
// when the committed transcript already ends in a Model-continuable state.
func (a *Session) Continue(ctx context.Context) (RunResult, error) {
	return a.submit(ctx, commandContinue, "Continue", Message{})
}

func (a *Session) submit(ctx context.Context, kind sessionCommandKind, operation string, message Message) (RunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := a.admissionError(operation); err != nil {
		return RunResult{}, err
	}

	select {
	case a.admission <- struct{}{}:
	default:
		return RunResult{}, runtimeError(CodeBusy, operation, "Session is already processing a Run", nil)
	}
	releaseAdmission := true
	defer func() {
		if releaseAdmission {
			<-a.admission
		}
	}()

	cmd := sessionCommand{kind: kind, ctx: ctx, message: message, result: make(chan promptResponse, 1)}
	select {
	case a.commands <- cmd:
		releaseAdmission = false
	case <-ctx.Done():
		return RunResult{}, runtimeError(codeForContext(ctx.Err()), operation, operation+" was canceled before admission", ctx.Err())
	case <-a.closeSignal:
		return RunResult{}, a.admissionError(operation)
	}

	select {
	case response := <-cmd.result:
		return response.result, response.err
	case <-ctx.Done():
		return RunResult{}, runtimeError(codeForContext(ctx.Err()), operation, operation+" context ended", ctx.Err())
	}
}

// Abort cancels the current Run, and writes an AbortRequested Event before the
// Run's RunEnded. It does not close the Session, and it is a no-op when no Run
// is active.
func (a *Session) Abort() { a.abort() }

// abort cancels the Run in flight, if any, and reports whether there was one.
func (a *Session) abort() bool {
	a.runMu.Lock()
	stop := a.currentRunStop
	header := NewHeader(a.id, a.currentRunID, a.currentTurn)
	if stop == nil || a.runEnding {
		// No Run, or one that is writing its end: an abort_requested after
		// run_ended would say the Abort came after the Run.
		a.runMu.Unlock()
		return false
	}
	// The Run stops at once; the Event is written beside it, and the Run's
	// last write waits for it. It is counted while the Run cannot yet be
	// ending.
	a.abortMu.Lock()
	a.aborts++
	a.abortMu.Unlock()
	a.runMu.Unlock()
	go func() {
		_ = a.writeNow(context.Background(), AbortRequested{Header: header})
		a.abortMu.Lock()
		a.aborts--
		a.abortCond.Broadcast()
		a.abortMu.Unlock()
	}()
	stop()
	return true
}

// waitForAborts blocks until every AbortRequested write has finished.
func (a *Session) waitForAborts() {
	a.abortMu.Lock()
	for a.aborts > 0 {
		a.abortCond.Wait()
	}
	a.abortMu.Unlock()
}

func (a *Session) admissionError(operation string) error {
	switch a.Status() {
	case SessionStatusClosing:
		return runtimeError(CodeSessionClosing, operation, "Session is closing", nil)
	case SessionStatusClosed:
		return runtimeError(CodeSessionClosed, operation, "Session is closed", nil)
	default:
		return nil
	}
}

// Close stops the Session: it takes no new Run, lets the one in flight finish
// (Abort ends it sooner), and drops the messages still queued. It returns when
// the Session's goroutine is done, or ctx.Err() if ctx ends first, and it is
// safe to call more than once.
func (a *Session) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	a.closeOnce.Do(func() {
		a.closeRequested.Store(true)
		a.setStatus(SessionStatusClosing)
		close(a.closeSignal)
	})
	select {
	case <-a.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// WaitForIdle blocks until the Session is idle. It returns ctx.Err() if ctx
// ends first, and an error when the Session is closing or closed.
func (a *Session) WaitForIdle(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		// Capture the change signal before reading the Status, so a status
		// transition that lands in between closes the captured channel and
		// wakes this loop instead of being missed.
		a.stateMu.Lock()
		changed := a.stateChange
		a.stateMu.Unlock()
		switch a.Status() {
		case SessionStatusIdle:
			return nil
		case SessionStatusCreated, SessionStatusBusy:
			select {
			case <-changed:
			case <-ctx.Done():
				return ctx.Err()
			}
		default:
			return a.admissionError("WaitForIdle")
		}
	}
}

func (a *Session) loop() {
	a.setStatus(SessionStatusIdle)
	close(a.ready)
	defer a.finishClose()

	for {
		select {
		case <-a.closeSignal:
			return
		case cmd := <-a.commands:
			switch cmd.kind {
			case commandPrompt, commandContinue:
				operation := "Prompt"
				if cmd.kind == commandContinue {
					operation = "Continue"
				}
				if err := a.admissionError(operation); err != nil {
					cmd.result <- promptResponse{err: err}
					<-a.admission
					continue
				}
				if cmd.kind == commandContinue {
					if err := validateContinuable(a.transcript.Messages()); err != nil {
						cmd.result <- promptResponse{err: err}
						<-a.admission
						continue
					}
				}
				a.setStatus(SessionStatusBusy)
				var prompt *Message
				if cmd.kind == commandPrompt {
					prompt = &cmd.message
				}
				result, err := a.executeRun(cmd.ctx, prompt)
				// Release admission and record Idle before delivering the
				// result: the caller that receives it must be able to start the
				// next Run immediately instead of observing this finished
				// Run's admission token as Busy.
				<-a.admission
				if a.closeRequested.Load() {
					cmd.result <- promptResponse{result: result, err: err}
					return
				}
				a.setStatus(SessionStatusIdle)
				cmd.result <- promptResponse{result: result, err: err}
			}
		}
	}
}

func (a *Session) finishClose() {
	a.dropLeftoverControl()
	a.setStatus(SessionStatusClosed)
	if a.ownFeed {
		// A Session that owns its Feed says it left, and ends its streams.
		// A host's Feed outlives the Session, and the host closes it.
		a.feed.Publish(SessionUnloaded{Header: NewHeader(a.id, "", 0), Why: "close"})
		a.feed.Close()
	}
	a.doneOnce.Do(func() { close(a.done) })
}

// dropLeftoverControl reports a message that waited in a queue when the Session
// closed, which no Run will take: one that arrived after its Run had written
// its last boundary. Each is a MessageDropped, so a caller that was told it
// was queued also learns that it was not taken.
func (a *Session) dropLeftoverControl() {
	for _, queue := range []chan control{a.steer, a.followUp} {
		for {
			select {
			case queued := <-queue:
				if queued.source == MessageSourceInterrupt {
					a.interruptTaken()
				}
				_ = a.writeNow(context.Background(), MessageDropped{Header: a.nowHeader(), MessageID: queued.message.ID, Source: queued.source, Why: "session_closed"})
				continue
			default:
			}
			break
		}
	}
	for _, queued := range a.takeNotices() {
		_ = a.writeNow(context.Background(), MessageDropped{Header: a.nowHeader(), MessageID: queued.message.ID, Source: MessageSourceNotice, Why: "session_closed"})
	}
}

func (a *Session) setStatus(status SessionStatus) {
	a.status.Store(encodeSessionStatus(status))
	a.stateMu.Lock()
	old := a.stateChange
	a.stateChange = make(chan struct{})
	close(old)
	a.stateMu.Unlock()
}

func encodeSessionStatus(status SessionStatus) uint32 {
	switch status {
	case SessionStatusCreated:
		return 1
	case SessionStatusIdle:
		return 2
	case SessionStatusBusy:
		return 3
	case SessionStatusClosing:
		return 4
	case SessionStatusClosed:
		return 5
	default:
		return 0
	}
}

func decodeSessionStatus(status uint32) SessionStatus {
	switch status {
	case 1:
		return SessionStatusCreated
	case 2:
		return SessionStatusIdle
	case 3:
		return SessionStatusBusy
	case 4:
		return SessionStatusClosing
	case 5:
		return SessionStatusClosed
	default:
		return SessionStatusCreated
	}
}

func (a *Session) commitMessage(message Message) error {
	return a.appendMessage(message, true)
}

// appendMessage commits one Message. enforce applies the Message and
// transcript limits; only the closing results of a failed Run skip them (see
// closeOpenCalls).
func (a *Session) appendMessage(message Message, enforce bool) error {
	if message.ID == "" {
		message.ID = MessageID(nextID("message"))
	}
	message = message.Clone()
	bytes, err := json.Marshal(message)
	if err != nil {
		return runtimeError(CodeInternalInvariant, "commitMessage", "cannot encode Message", err)
	}
	if enforce && limitExceededUint32(a.limits.MaxMessages, uint32(a.transcript.Len()+1)) {
		return runtimeError(CodeLimitExceeded, "commitMessage", "maximum Messages exceeded", nil)
	}
	if enforce && limitExceededUint64(a.limits.MaxMessageBytes, uint64(len(bytes))) {
		return runtimeError(CodeLimitExceeded, "commitMessage", "maximum Message bytes exceeded", nil)
	}
	// The transcript bound is tracked incrementally: one JSON array of n
	// elements costs the elements plus n-1 separators and two brackets.
	candidate := a.transcriptBytes + uint64(len(bytes))
	if a.transcript.Len() > 0 {
		candidate++
	}
	if enforce && limitExceededUint64(a.limits.MaxTranscriptBytes, candidate+2) {
		return runtimeError(CodeLimitExceeded, "commitMessage", "maximum transcript bytes exceeded", nil)
	}
	if err := a.transcript.Append(message); err != nil {
		return runtimeError(CodeInternalInvariant, "commitMessage", "cannot append to transcript: "+err.Error(), err)
	}
	a.transcriptBytes = candidate
	return nil
}

// measureTranscript recomputes the encoded transcript size once per Run so an
// externally mutated Transcript (a compacted or reloaded Session) is bounded
// correctly without re-encoding on every commit.
func (a *Session) measureTranscript() error {
	messages := a.transcript.Messages()
	if len(messages) == 0 {
		a.transcriptBytes = 0
		return nil
	}
	encoded, err := json.Marshal(messages)
	if err != nil {
		return runtimeError(CodeInternalInvariant, "Run", "cannot encode transcript", err)
	}
	// Exclude the surrounding brackets; commitMessage adds them back.
	a.transcriptBytes = uint64(len(encoded)) - 2
	return nil
}

// syncLoadedTools sets the loaded Tools to the ones a rewritten Transcript
// lists, so the Session offers the Tools a load would: a compaction unloads
// the Tools its Segment and kept tail did not call. A Tool of a ToolSet the
// Agent installed active is never loaded or unloaded.
func (a *Session) syncLoadedTools() error {
	source, ok := a.transcript.(LoadedToolSource)
	if !ok {
		return nil
	}
	a.registryMu.Lock()
	defer a.registryMu.Unlock()
	return a.registry.syncLoaded(source.LoadedTools())
}

func addUsage(total, current Usage) Usage {
	total.InputTokens += current.InputTokens
	total.OutputTokens += current.OutputTokens
	total.TotalTokens += current.TotalTokens
	total.CacheReadTokens += current.CacheReadTokens
	total.CacheWriteTokens += current.CacheWriteTokens
	return total
}

// cutOffReply is what a Turn keeps of a reply whose stream broke: the text
// received so far, marked StopReasonCutOff, or no Message when no text arrived.
// Reasoning and Tool calls are left out: a provider takes reasoning back only
// whole and signed, and a Tool call that is never answered cannot be sent.
func cutOffReply(partial Message) Message {
	parts := make([]ContentPart, 0, len(partial.Parts))
	for _, part := range partial.Parts {
		if part.Kind == ContentKindText && part.Text != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return Message{}
	}
	return Message{ID: partial.ID, RunID: partial.RunID, Role: RoleAssistant, Parts: coalesceDeltas(parts), StopReason: StopReasonCutOff, Model: partial.Model}
}

// coalesceDeltas merges the Parts that streamed deltas produced into one Part
// per contiguous block of text or of reasoning, so a reply is stored and
// replayed as the blocks the provider sent rather than as its fragments. A
// Reasoning block closes when its signature arrives
// (ModelEventKindReasoningDone), so a following Reasoning delta starts a new
// block. Other Parts are kept as is.
func coalesceDeltas(parts []ContentPart) []ContentPart {
	if len(parts) < 2 {
		return parts
	}
	out := make([]ContentPart, 0, len(parts))
	var text strings.Builder
	open := -1 // index in out of the block still receiving deltas
	flush := func() {
		if open >= 0 {
			out[open].Text = text.String()
			text.Reset()
			open = -1
		}
	}
	for _, part := range parts {
		mergeable := (part.Kind == ContentKindText || part.Kind == ContentKindReasoning) && part.Data == nil && part.MIMEType == "" && part.Metadata == nil
		if open >= 0 && mergeable && part.Kind == out[open].Kind {
			text.WriteString(part.Text)
			out[open].Signature = part.Signature
		} else {
			flush()
			out = append(out, part)
			if mergeable {
				open = len(out) - 1
				text.WriteString(part.Text)
			}
		}
		if open >= 0 && len(out[open].Signature) > 0 {
			flush()
		}
	}
	flush()
	return out
}

func executeToolSafely(ctx context.Context, tool Tool, use ToolUse, progress ToolProgressFunc) (result ToolResult, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			// Match the Extension guard: a short, client-safe Message with the
			// captured stack attached to the Cause for logs.
			err = runtimeError(CodeToolExecutionFailure, "Tool",
				fmt.Sprintf("tool panic: %v", recovered),
				fmt.Errorf("tool panic: %v\n%s", recovered, debug.Stack()))
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
		return runtimeError(CodeModelFailure, "Model", "unknown Model failure", nil)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return runtimeError(codeForContext(err), "Model", err.Error(), err)
	}
	var overflow interface{ ContextOverflow() bool }
	if errors.As(err, &overflow) && overflow.ContextOverflow() {
		return runtimeError(CodeContextOverflow, "Model", err.Error(), err)
	}
	failure := runtimeError(CodeModelFailure, "Model", err.Error(), err)
	// A Model that knows the failure is transient (a rate limit, an overload,
	// a broken stream) says so with `Retryable() bool`, as the gateway's
	// Error does. The Model was called, so the request is not "not run".
	var transient interface{ Retryable() bool }
	if errors.As(err, &transient) && transient.Retryable() {
		failure.Retryable = true
	}
	return failure
}

func validateLimits(limits CoreLimits) error {
	if limits.ModelCallDeadline < 0 || limits.ToolCallDeadline < 0 {
		return runtimeError(CodeInvalidArgument, "CoreLimits", "deadlines cannot be negative", nil)
	}
	return nil
}

func validatePrompt(message Message) error {
	if message.Role != RoleUser {
		return runtimeError(CodeInvalidArgument, "Prompt", "Prompt message must have role user", nil)
	}
	if !hasContent(message) {
		return runtimeError(CodeInvalidArgument, "Prompt", "Prompt message is empty", nil)
	}
	return nil
}

func validateControlMessage(message Message, operation string) error {
	if message.Role != RoleUser {
		return runtimeError(CodeInvalidArgument, operation, operation+" message must have role user", nil)
	}
	if !hasContent(message) {
		return runtimeError(CodeInvalidArgument, operation, operation+" message is empty", nil)
	}
	return nil
}

// hasContent reports whether at least one Part carries substance: non-blank
// text, or binary/JSON data. A Message made only of blank text Parts is empty.
func hasContent(message Message) bool {
	for _, part := range message.Parts {
		switch part.Kind {
		case ContentKindText, ContentKindReasoning:
			if strings.TrimSpace(part.Text) != "" {
				return true
			}
		default:
			if len(part.Data) > 0 || strings.TrimSpace(part.Text) != "" {
				return true
			}
		}
	}
	return false
}

// refreshToolSets resolves the ToolSets again at the start of a Run (the first
// Run uses NewSession's resolution), so a change on a ToolSet's side takes
// effect at this Run and no later than the next. A ToolSet that fails is
// skipped with an ExtensionFailed Event; a loaded Tool its ToolSet no longer
// carries is unloaded with a stored tool_unloaded Event (why: gone), which the
// Record folds, so every instance that loads the Record offers the same Tools.
func (a *Session) refreshToolSets(ctx context.Context, header Header) *RuntimeError {
	if len(a.registry.sets) == 0 {
		return nil
	}
	results, err := a.registry.resolveAll(ctx)
	if err != nil {
		return asRuntimeError(err)
	}
	source, _ := a.transcript.(LoadedToolSource)
	a.registryMu.Lock()
	report, err := a.registry.apply(results, source)
	a.registryMu.Unlock()
	if err != nil {
		return asRuntimeError(err)
	}
	for _, name := range report.unavailable {
		a.stage(ExtensionFailed{Header: header, Stage: "toolset:" + name, Code: CodeToolResolutionFailure})
	}
	for _, id := range report.gone {
		a.stage(ToolUnloaded{Header: header, ToolID: id, Why: ToolUnloadedWhyGone})
	}
	return nil
}

// refreshTools re-reads every ToolSource. Static Tools and ToolSets are
// untouched; only the dynamic part of the registry is rebuilt.
func (a *Session) refreshTools() error {
	if !a.registry.hasSources() {
		return nil
	}
	a.registryMu.Lock()
	defer a.registryMu.Unlock()
	return a.registry.refreshSources()
}

// validateContinuable enforces that Continue never synthesizes user input: the
// transcript must already end where a Model can pick up.
func validateContinuable(messages []Message) error {
	if len(messages) == 0 {
		return runtimeError(CodeInvalidState, "Continue", "transcript is empty", nil)
	}
	// A reply cut off mid-stream is never sent again, so Continue asks the
	// Model again from the Message before it.
	last := len(messages) - 1
	for last >= 0 && messages[last].StopReason == StopReasonCutOff {
		last--
	}
	if last < 0 {
		return runtimeError(CodeInvalidState, "Continue", "transcript does not end in a Model-continuable state", nil)
	}
	switch messages[last].Role {
	case RoleUser, RoleToolResult, RoleNotice:
		return nil
	default:
		return runtimeError(CodeInvalidState, "Continue", "transcript does not end in a Model-continuable state", nil)
	}
}

// controlCapacity turns a configured control-message limit into a channel
// capacity.
func controlCapacity(limit uint32) int {
	if limit > maxControlCapacity {
		return maxControlCapacity
	}
	return int(limit)
}

const maxControlCapacity = 1024

func limitExceededUint32(limit, value uint32) bool {
	return value > limit
}

func limitExceededUint64(limit, value uint64) bool {
	return value > limit
}

func safeError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// asError returns failure as an error that is nil when failure is nil. Code
// that holds a *RuntimeError and hands it on as an error goes through it, so
// a nil failure never becomes a non-nil error (the typed-nil trap).
func asError(failure *RuntimeError) error {
	if failure == nil {
		return nil
	}
	return failure
}

func asRuntimeError(err error) *RuntimeError {
	if err == nil {
		return nil
	}
	var runtimeErr *RuntimeError
	if errors.As(err, &runtimeErr) {
		return runtimeErr
	}
	return runtimeError(CodeInternalInvariant, "Core", err.Error(), err)
}
