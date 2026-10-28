package gotato

import (
	"context"
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

// NewSession builds a live Session from an Agent's parts, given as options,
// and starts it. A Model is required. The Session takes its ID from its
// Transcript when the Transcript has an ID, as a session.Record does, and
// generates one otherwise. Close it when it is no longer needed.
//
// ctx covers the work NewSession does before the Session exists: resolving
// the ToolSets installed active and the ones the Session's earlier Runs
// activated. When ctx ends first, it fails with canceled or deadline_exceeded
// and builds nothing. ctx does not outlive the call; the Session's own Runs
// take their contexts as before.
func NewSession(ctx context.Context, options ...SessionOption) (*Session, error) {
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
// Continue, Steer, Interrupt, FollowUp and Abort drive its Loop; Subscribe
// and SubscribeRun stream its Events; Status and Done report its life; Tools
// and InspectContext show what its next Turn would send.
//
// The Runner builds one from a session.Record when a Run needs it, keeps it
// warm in memory between Runs (service.WarmPolicy), and closes it when it
// leaves memory. The Record stays the truth.
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
	// handoff keeps the messages still queued when a Run completes or fails,
	// for the host to take (TakeQueued); see WithQueueHandoff.
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

// sessionCommandKind says which Run a sessionCommand starts.
type sessionCommandKind uint8

const (
	commandPrompt sessionCommandKind = iota + 1
	commandContinue
)

// sessionCommand is a Prompt or Continue handed to the Session goroutine; the
// goroutine answers on result.
type sessionCommand struct {
	kind    sessionCommandKind
	ctx     context.Context
	message Message
	result  chan promptResponse
}

// promptResponse is the answer to a sessionCommand.
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
// CodeSessionClosed after Close. Canceling ctx aborts the Run, and Prompt
// then returns the Run's settled result, with status canceled or
// deadline_exceeded, once the Run has ended under ctx.
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

// submit takes the admission token, hands a command to the Session goroutine
// and waits for its RunResult. Only one Run is admitted at a time (CodeBusy).
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
	}
	// The Run ends with ctx, since it runs under it, so wait for its settled
	// result: it carries the status that says why. A Model or Tool that
	// ignores ctx cannot hold the caller past settleGrace; the caller then
	// gets the ctx error alone, and the Run settles later.
	grace := time.NewTimer(settleGrace)
	defer grace.Stop()
	select {
	case response := <-cmd.result:
		return response.result, response.err
	case <-grace.C:
		return RunResult{}, runtimeError(codeForContext(ctx.Err()), operation, operation+" context ended", ctx.Err())
	}
}

// settleGrace is how long Prompt and Continue wait, after their ctx ends, for
// the Run to settle under it.
const settleGrace = 5 * time.Second
