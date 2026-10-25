package gotato

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// SessionStatus is where a live Session is in its life: created, idle while
// it waits for a Run, busy during one, then closing and closed.
type SessionStatus string

const (
	SessionStatusCreated SessionStatus = "created"
	SessionStatusIdle    SessionStatus = "idle"
	SessionStatusBusy    SessionStatus = "busy"
	SessionStatusClosing SessionStatus = "closing"
	SessionStatusClosed  SessionStatus = "closed"
)

// Option configures a Session built by NewSession.
type Option func(*sessionConfig) error

type sessionConfig struct {
	model          Model
	instruction    string
	tools          []Tool
	toolSets       []toolSetConfig
	toolSources    []ToolSource
	rootNamespace  string
	extensions     extensionSet
	limits         CoreLimits
	limitsSet      bool
	transcript     Transcript
	contextBuilder ContextBuilder
	modelOptions   ModelOptions
	cacheKey       string
	maxEffect      ToolEffect
	feed           *Feed
	onRunStart     func(RunID)
}

// WithFeed has the Session publish its Events to feed instead of a Feed of
// its own, so the host that built it, such as a service.Runner, can offer
// the Session's stream across Runs. The host owns the Feed and closes it; it
// also sets the Feed's source, which is where a subscriber that falls behind
// catches up.
func WithFeed(feed *Feed) Option {
	return func(c *sessionConfig) error {
		if feed == nil {
			return runtimeError(ErrInvalidArgument, "WithFeed", "feed is nil", nil)
		}
		c.feed = feed
		return nil
	}
}

// WithRunStart calls fn on the Session's goroutine with each Run's ID, as the
// Run starts and before its first Event. fn must not block. A host uses it
// to find the Run it started.
func WithRunStart(fn func(RunID)) Option {
	return func(c *sessionConfig) error { c.onRunStart = fn; return nil }
}

func WithModel(model Model) Option {
	return func(c *sessionConfig) error {
		if model == nil {
			return runtimeError(ErrInvalidArgument, "WithModel", "model is nil", nil)
		}
		c.model = model
		return nil
	}
}

func WithInstruction(instruction string) Option {
	return func(c *sessionConfig) error { c.instruction = instruction; return nil }
}

// WithModelOptions sets the sampling and reasoning hints the Session sends on
// every model call. Provider adapters map the fields they support.
func WithModelOptions(options ModelOptions) Option {
	return func(c *sessionConfig) error {
		if options.Temperature != nil && *options.Temperature < 0 {
			return runtimeError(ErrInvalidArgument, "WithModelOptions", "temperature is negative", nil)
		}
		if options.Temperature != nil {
			temperature := *options.Temperature
			options.Temperature = &temperature
		}
		c.modelOptions = options
		return nil
	}
}

// WithCacheKey sets the ModelRequest.CacheKey the Session sends on every model
// call: a stable value for one conversation, such as a hash of its Session ID,
// so the provider serves its Turns from one prompt cache. It never changes the
// prompt.
func WithCacheKey(key string) Option {
	return func(c *sessionConfig) error { c.cacheKey = key; return nil }
}

// WithMaxEffect caps the Effect of the Tools the Session offers. A Tool above
// the cap, including one in a ToolSet activated later, is never shown to the
// Model, and a call to it fails as a call to an unknown Tool. A Tool that
// declares no Effect ranks as EffectDestructive, so any lower cap hides it.
// EffectUnspecified means no cap, which is the default.
func WithMaxEffect(effect ToolEffect) Option {
	return func(c *sessionConfig) error {
		if !effect.Valid() {
			return runtimeError(ErrInvalidArgument, "WithMaxEffect", "unknown Effect "+string(effect), nil)
		}
		c.maxEffect = effect
		return nil
	}
}

func WithTool(tool Tool) Option {
	return func(c *sessionConfig) error {
		if tool == nil {
			return runtimeError(ErrInvalidArgument, "WithTool", "tool is nil", nil)
		}
		c.tools = append(c.tools, tool)
		return nil
	}
}

func WithTools(tools ...Tool) Option {
	return func(c *sessionConfig) error {
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
	return func(c *sessionConfig) error {
		if limits.ModelCallDeadline < 0 || limits.ToolCallDeadline < 0 {
			return runtimeError(ErrInvalidArgument, "WithLimits", "deadlines cannot be negative", nil)
		}
		c.limits = limits
		c.limitsSet = true
		return nil
	}
}

// WithDeadlines overrides only the Model call and Tool call deadlines. A zero
// duration disables that deadline. Other local limits retain their defaults
// and are not made explicit by this option. A Run's own deadline is its ctx.
func WithDeadlines(model, tool time.Duration) Option {
	return func(c *sessionConfig) error {
		if model < 0 || tool < 0 {
			return runtimeError(ErrInvalidArgument, "WithDeadlines", "deadlines cannot be negative", nil)
		}
		c.limits.ModelCallDeadline = model
		c.limits.ToolCallDeadline = tool
		return nil
	}
}

// NewSession builds a live Session from an Agent's parts, given as options,
// and starts it. A Model is required. The Session takes its ID from its
// Transcript when the Transcript has an ID, as a session.Record does, and
// generates one otherwise. Close it when it is no longer needed.
//
// NewSession cannot be cancelled: it resolves ToolSets with a background
// context. Prefer NewSessionContext.
func NewSession(options ...Option) (*Session, error) {
	return NewSessionContext(context.Background(), options...)
}

// NewSessionContext is NewSession with a context for the work it does before
// the Session exists: resolving the ToolSets installed active and the ones
// the Session's earlier Runs activated. When ctx ends first, it fails with
// cancelled or deadline_exceeded and builds nothing. ctx does not outlive the
// call; the Session's own Runs take their contexts as before.
func NewSessionContext(ctx context.Context, options ...Option) (*Session, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, runtimeError(codeForContext(err), "NewSession", "context ended", err)
	}
	cfg := sessionConfig{limits: defaultLimits()}
	for _, option := range options {
		if option == nil {
			continue
		}
		if err := option(&cfg); err != nil {
			return nil, err
		}
	}
	if cfg.model == nil {
		return nil, runtimeError(ErrInvalidArgument, "NewSession", "a Model is required", nil)
	}
	if err := validateLimits(cfg.limits); err != nil {
		return nil, err
	}

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
		limitsSet:      cfg.limitsSet,
		modelOptions:   cfg.modelOptions,
		cacheKey:       cfg.cacheKey,
		commands:       make(chan sessionCommand),
		closeSignal:    make(chan struct{}),
		done:           make(chan struct{}),
		ready:          make(chan struct{}),
		admission:      make(chan struct{}, 1),
		steer:          make(chan Message, controlCapacity(cfg.limits.MaxSteerMessages)),
		followUp:       make(chan Message, controlCapacity(cfg.limits.MaxFollowUpMessages)),
		stateChange:    make(chan struct{}),
		onRunStart:     cfg.onRunStart,
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
			a.feed.SetSource(source)
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
// Continue, Steer, FollowUp and Abort drive its Loop; Subscribe, SubscribeRun
// and SubscribeFrom stream its Events; Status and Done report its life; Tools
// and InspectContext show what its next Turn would send.
//
// Until the Runner keeps Sessions warm, it builds one from a session.Record
// for each Run and closes it afterwards; the Record stays the truth.
type Session struct {
	id          SessionID
	model       Model
	instruction string
	registry    *toolRegistry
	registryMu  sync.Mutex
	extensions  extensionSet
	limits      CoreLimits
	limitsSet   bool
	// modelOptions and cacheKey are sent unchanged with every model call.
	modelOptions ModelOptions
	cacheKey     string

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
	steer       chan Message
	followUp    chan Message

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

func (a *Session) ID() SessionID { return a.id }

func (a *Session) Status() SessionStatus {
	status := decodeSessionStatus(a.status.Load())
	if a.closeRequested.Load() && status != SessionStatusClosed {
		return SessionStatusClosing
	}
	return status
}

func (a *Session) Done() <-chan struct{} { return a.done }

func (a *Session) CancelRun(ctx context.Context, runID RunID) error {
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
	a.runMu.Unlock()
	if currentID != runID || !a.abort() {
		return runtimeError(ErrInvalidState, "CancelRun", "Run is not active", nil)
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
	return a.feed.Subscribe(ctx, NextRun()...)
}

// SubscribeFrom starts a Subscription to the Session's stream after the
// stored Event numbered after: the later Events first, from the Record, and
// then the live ones. A client that was cut off reconnects with the Number of
// the last Event it saw. SubscribeFrom(ctx, 0) starts at the Session's first
// Event.
func (a *Session) SubscribeFrom(ctx context.Context, after uint64) (Subscription, error) {
	return a.feed.Subscribe(ctx, SubscribeAfter(after))
}

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
		return RunResult{}, runtimeError(ErrBusy, operation, "Session is already processing a Run", nil)
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
		return RunResult{}, runtimeError(codeForContext(ctx.Err()), operation, operation+" was cancelled before admission", ctx.Err())
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

// Steer delivers guidance for the Run in flight. The Session commits it at the
// next Turn boundary; it never interrupts the current Model or Tool work. A
// Steer that arrives where the Run would settle keeps the Run going for one
// more Turn rather than being dropped. Its way into the Session is reported
// as Events: MessageQueued, and then MessageCommitted, or MessageDropped when
// the Run ends first, or MessageRejected when the queue is full.
func (a *Session) Steer(message Message) error {
	return a.enqueueControl(a.steer, message, "Steer", SourceSteer)
}

// FollowUp supplies the continuation to use where the Run would otherwise
// settle. It is not a general external request queue: the buffer is bounded
// and belongs to this Session.
func (a *Session) FollowUp(message Message) error {
	return a.enqueueControl(a.followUp, message, "FollowUp", SourceFollowUp)
}

func (a *Session) enqueueControl(target chan Message, message Message, operation string, source MessageSource) error {
	if err := validateControlMessage(message, operation); err != nil {
		return err
	}
	if err := a.admissionError(operation); err != nil {
		return err
	}
	message = message.Clone()
	if message.ID == "" {
		message.ID = MessageID(nextID("message"))
	}
	reject := func(why string, err error) error {
		// A refusal is reported as an Event, written on a best-effort basis:
		// the caller already gets the error.
		_ = a.writeNow(context.Background(), MessageRejected{Header: a.nowHeader(), MessageID: message.ID, Source: source, Why: why})
		return err
	}
	if cap(target) == 0 {
		return reject("disabled", runtimeError(ErrLimitExceeded, operation, operation+" is disabled by the configured limit", nil))
	}
	if len(target) >= cap(target) {
		return reject("queue_full", runtimeError(ErrLimitExceeded, operation, operation+" buffer is full", nil))
	}
	// The message is written as queued before it can be committed, so the
	// Record never shows a commit with no queueing.
	if err := a.writeNow(context.Background(), MessageQueued{Header: a.nowHeader(), MessageID: message.ID, Source: source}); err != nil {
		return err
	}
	select {
	case target <- message:
		return nil
	default:
		return reject("queue_full", runtimeError(ErrLimitExceeded, operation, operation+" buffer is full", nil))
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
		return runtimeError(ErrSessionClosing, operation, "Session is closing", nil)
	case SessionStatusClosed:
		return runtimeError(ErrSessionClosed, operation, "Session is closed", nil)
	default:
		return nil
	}
}

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
	a.setStatus(SessionStatusClosed)
	if a.ownFeed {
		// A Session that owns its Feed says it left, and ends its streams.
		// A host's Feed outlives the Session, and the host closes it.
		a.feed.Publish(SessionUnloaded{Header: NewHeader(a.id, "", 0), Why: "close"})
		a.feed.Close()
	}
	a.doneOnce.Do(func() { close(a.done) })
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
		return runtimeError(ErrInternalInvariant, "commitMessage", "cannot encode Message", err)
	}
	if enforce && limitExceededUint32(a.limitsSet, a.limits.MaxMessages, uint32(a.transcript.Len()+1)) {
		return runtimeError(ErrLimitExceeded, "commitMessage", "maximum Messages exceeded", nil)
	}
	if enforce && limitExceededUint64(a.limitsSet, a.limits.MaxMessageBytes, uint64(len(bytes))) {
		return runtimeError(ErrLimitExceeded, "commitMessage", "maximum Message bytes exceeded", nil)
	}
	// The transcript bound is tracked incrementally: one JSON array of n
	// elements costs the elements plus n-1 separators and two brackets.
	candidate := a.transcriptBytes + uint64(len(bytes))
	if a.transcript.Len() > 0 {
		candidate++
	}
	if enforce && limitExceededUint64(a.limitsSet, a.limits.MaxTranscriptBytes, candidate+2) {
		return runtimeError(ErrLimitExceeded, "commitMessage", "maximum transcript bytes exceeded", nil)
	}
	if err := a.transcript.Append(message); err != nil {
		return runtimeError(ErrInternalInvariant, "commitMessage", "cannot append to transcript: "+err.Error(), err)
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
		return runtimeError(ErrInternalInvariant, "Run", "cannot encode transcript", err)
	}
	// Exclude the surrounding brackets; commitMessage adds them back.
	a.transcriptBytes = uint64(len(encoded)) - 2
	return nil
}

// syncToolSets switches off the ToolSets a rewritten Transcript no longer lists
// as active, so the Session offers the ones a load would. A ToolSet the Agent
// installed active stays on.
func (a *Session) syncToolSets() error {
	source, ok := a.transcript.(ActiveToolSetSource)
	if !ok {
		return nil
	}
	a.registryMu.Lock()
	defer a.registryMu.Unlock()
	return a.registry.narrow(source.ActiveToolSets())
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
// received so far, marked StopCutOff, or no Message when no text arrived.
// Reasoning and Tool calls are left out: a provider takes reasoning back only
// whole and signed, and a Tool call that is never answered cannot be sent.
func cutOffReply(partial Message) Message {
	parts := make([]ContentPart, 0, len(partial.Parts))
	for _, part := range partial.Parts {
		if part.Kind == ContentText && part.Text != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return Message{}
	}
	return Message{ID: partial.ID, RunID: partial.RunID, Role: RoleAssistant, Parts: coalesceDeltas(parts), StopReason: StopCutOff, Model: partial.Model}
}

// coalesceDeltas merges the Parts that streamed deltas produced into one Part
// per contiguous block of text or of reasoning, so a reply is stored and
// replayed as the blocks the provider sent rather than as its fragments. A
// Reasoning block closes when its signature arrives (ModelReasoningDone), so
// a following Reasoning delta starts a new block. Other Parts are kept as is.
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
		mergeable := (part.Kind == ContentText || part.Kind == ContentReasoning) && part.Data == nil && part.MIMEType == "" && part.Metadata == nil
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

func executeToolSafely(tool Tool, ctx context.Context, use ToolUse, progress ToolProgressFunc) (result ToolResult, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			// Match the Extension guard: a short, client-safe Message with the
			// captured stack attached to the Cause for logs.
			err = runtimeError(ErrToolExecutionFailure, "Tool",
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
		return runtimeError(ErrModelFailure, "Model", "unknown Model failure", nil)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return runtimeError(codeForContext(err), "Model", err.Error(), err)
	}
	var overflow interface{ ContextOverflow() bool }
	if errors.As(err, &overflow) && overflow.ContextOverflow() {
		return runtimeError(ErrContextOverflow, "Model", err.Error(), err)
	}
	failure := runtimeError(ErrModelFailure, "Model", err.Error(), err)
	// A Model that knows the failure is transient (a rate limit, an overload,
	// a broken stream) says so with `Retryable() bool`, as the gateway's
	// Error does. The Model was called, so the request is not "not run".
	var transient interface{ IsRetryable() bool }
	if errors.As(err, &transient) && transient.IsRetryable() {
		failure.Retryable = true
	}
	return failure
}

func validateLimits(limits CoreLimits) error {
	if limits.ModelCallDeadline < 0 || limits.ToolCallDeadline < 0 {
		return runtimeError(ErrInvalidArgument, "CoreLimits", "deadlines cannot be negative", nil)
	}
	return nil
}

func validatePrompt(message Message) error {
	if message.Role != RoleUser {
		return runtimeError(ErrInvalidArgument, "Prompt", "Prompt message must have role user", nil)
	}
	if !hasContent(message) {
		return runtimeError(ErrInvalidArgument, "Prompt", "Prompt message is empty", nil)
	}
	return nil
}

func validateControlMessage(message Message, operation string) error {
	if message.Role != RoleUser {
		return runtimeError(ErrInvalidArgument, operation, operation+" message must have role user", nil)
	}
	if !hasContent(message) {
		return runtimeError(ErrInvalidArgument, operation, operation+" message is empty", nil)
	}
	return nil
}

// hasContent reports whether at least one Part carries substance: non-blank
// text, or binary/JSON data. A Message made only of blank text Parts is empty.
func hasContent(message Message) bool {
	for _, part := range message.Parts {
		switch part.Kind {
		case ContentText, ContentReasoning:
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

// buildContext runs the ContextBuilder under the same panic guard as an
// Extension and falls back to the snapshot's system instructions when the
// builder leaves them empty.
func (a *Session) buildContext(ctx context.Context, snapshot ContextSnapshot) (ModelContext, *RuntimeError) {
	var built ModelContext
	if err := guard("ContextBuilder", func() error {
		result, failure := a.contextBuilder.Build(ctx, snapshot)
		built = result
		return failure
	}); err != nil {
		return ModelContext{}, err
	}
	if built.SystemInstructions == "" {
		built.SystemInstructions = snapshot.SystemInstructions
	}
	if built.Messages == nil {
		built.Messages = []Message{}
	}
	return built, nil
}

// assembleTurn builds the request one Turn sends. The ContextBuilder decides
// what the Model sees now, and the ContextTransformer and MessageConverter
// Extensions shape that view further. Neither output is ever committed as the
// transcript: Session is what happened, Context is what the Model sees now.
// The request is laid out for prompt caching: static system and tools first,
// the append-only history last. A Turn and InspectContext both call it, so an
// inspection shows the bytes a Turn sends.
func (a *Session) assembleTurn(ctx context.Context, snapshot ContextSnapshot) (ModelContext, ModelRequest, string, *RuntimeError) {
	built, err := a.buildContext(ctx, snapshot)
	if err != nil {
		return ModelContext{}, ModelRequest{}, "", err
	}
	if !a.extensions.empty() {
		view := snapshot
		view.SystemInstructions = built.SystemInstructions
		view.Messages = built.Messages
		transformed, err := a.extensions.transformContext(ctx, view)
		if err != nil {
			return ModelContext{}, ModelRequest{}, "", err
		}
		built.Messages = transformed
	}
	request, prefix := AssembleRequest(built, a.registry.visibleSpecs())
	request.Options = a.modelOptions
	request.CacheKey = a.cacheKey
	return built, request, prefix, nil
}

// InspectContext builds the request the Session's next Turn would send for its
// Transcript as committed now, without calling the Model. It runs the code a
// Turn runs: the Tool sources are read again, the ContextBuilder builds the
// Context, the ContextTransformer and MessageConverter Extensions shape it,
// and the request carries the visible Tools and the model options. It runs no
// TurnPreparer and emits no Event. It fails with ErrBusy while a Run is in
// flight.
//
// The snapshot it builds from has no RunID and is Turn 1, as at the start of
// a Run. A ContextTransformer whose output depends on the Run or the Turn, or
// a ToolSet the Model activated during a Run, makes a later Turn differ.
func (a *Session) InspectContext(ctx context.Context) (ContextInspection, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ContextInspection{}, runtimeError(codeForContext(err), "InspectContext", "context ended", err)
	}
	if err := a.admissionError("InspectContext"); err != nil {
		return ContextInspection{}, err
	}
	// Holding the admission token keeps a Run from starting while the
	// Context is built from the Transcript and the Tool registry.
	select {
	case a.admission <- struct{}{}:
	default:
		return ContextInspection{}, runtimeError(ErrBusy, "InspectContext", "Session is processing a Run", nil)
	}
	defer func() { <-a.admission }()
	if err := a.refreshTools(); err != nil {
		return ContextInspection{}, asRuntimeError(err)
	}
	snapshot := ContextSnapshot{
		SessionID:          a.id,
		Turn:               1,
		SystemInstructions: a.instruction,
		Messages:           cloneMessages(a.transcript.Messages()),
	}
	built, request, prefix, err := a.assembleTurn(ctx, snapshot)
	if err != nil {
		return ContextInspection{}, err
	}
	return ContextInspection{
		SourceMessages: len(snapshot.Messages),
		Context:        built,
		Request:        request,
		PrefixHash:     prefix,
	}, nil
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
		return runtimeError(ErrInvalidState, "Continue", "transcript is empty", nil)
	}
	// A reply cut off mid-stream is never sent again, so Continue asks the
	// Model again from the Message before it.
	last := len(messages) - 1
	for last >= 0 && messages[last].StopReason == StopCutOff {
		last--
	}
	if last < 0 {
		return runtimeError(ErrInvalidState, "Continue", "transcript does not end in a Model-continuable state", nil)
	}
	switch messages[last].Role {
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
	if !spec.Effect.Valid() {
		return runtimeError(ErrInvalidArgument, "ToolSpec", "unknown Effect "+string(spec.Effect), nil)
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
	case "number":
		_, ok := value.(float64)
		return ok
	case "integer":
		number, ok := value.(float64)
		return ok && number == math.Trunc(number)
	case "null":
		return value == nil
	default:
		return true
	}
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
		out[i].Signature = slices.Clone(part.Signature)
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
