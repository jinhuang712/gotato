package gotato

import (
	"context"
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
// Transcript when the Transcript declares one (Capabilities.ID, as a
// record.Record does), and generates one otherwise. A single-valued option
// given twice fails with invalid_argument, naming it, and so does a Transcript
// that declares Durable without a Journal, Events and Loaded. Close it when it is no longer needed.
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
		e := runtimeError(codeForContext(err), "NewSession", "context ended", err)
		e.NotRun = true
		return nil, e
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
	cfg.limits = cfg.limits.withDefaults()
	spillPage, err := cfg.installReadBack()
	if err != nil {
		return nil, err
	}
	// The Model's own sampling defaults sit under what WithSampling sets, so
	// every request carries the whole of it.
	var modelSampling Sampling
	if err := callHost("Model.Config", func() error { modelSampling = cfg.model.Config().Sampling; return nil }); err != nil {
		return nil, runtimeError(CodeInvalidArgument, "NewSession", "the Model's Config failed", err)
	}
	cfg.sampling = cfg.sampling.over(modelSampling)

	if cfg.transcript == nil {
		cfg.transcript = &memoryTranscript{}
	}
	// The Transcript declares what it can do; one that stores must declare
	// every part a store needs, or it is refused here and not weeks later.
	var caps Capabilities
	if err := callHost("Transcript.Capabilities", func() error { caps = cfg.transcript.Capabilities(); return nil }); err != nil {
		return nil, runtimeError(CodeInvalidArgument, "NewSession", "the Transcript's Capabilities failed", err)
	}
	if err := caps.check(); err != nil {
		return nil, err
	}
	registry, err := newToolRegistry(ctx, &cfg, caps)
	if err != nil {
		return nil, err
	}
	if cfg.contextBuilder == nil {
		cfg.contextBuilder = FullHistoryContext()
	}
	id := SessionID(nextID("session"))
	if caps.ID != "" {
		id = SessionID(caps.ID)
	}

	a := &Session{
		def: sessionDef{
			id: id, model: cfg.model, instruction: cfg.instruction, extensions: cfg.extensions, limits: cfg.limits,
			spillPage: spillPage, sampling: cfg.sampling, cacheKey: cfg.cacheKey, contextBuilder: cfg.contextBuilder,
		},
		tools:   sessionTools{registry: registry},
		history: sessionHistory{transcript: cfg.transcript},
		queue:   newControlQueue(controlCapacity(cfg.limits.MaxSteerMessages), controlCapacity(cfg.limits.MaxFollowUpMessages), controlCapacity(cfg.limits.MaxNotices)),
		host:    sessionHost{onRunStart: cfg.onRunStart, handoff: cfg.handoff, requestID: cfg.requestID},
	}
	if caps.Journal != nil {
		a.events.journal = caps.Journal
	} else {
		// A Transcript that keeps no Events: they are numbered in memory,
		// after the last number of a Feed the host shares with other Runs.
		journal := &memoryJournal{}
		if cfg.feed != nil {
			journal.last = cfg.feed.lastNumber()
		}
		a.events.journal = journal
	}
	a.events.feed = cfg.feed
	// A host's Feed is sent its stored Events by the host's Journal; the
	// Session sends only those it numbers itself (see sessionEvents.sendStored).
	a.events.sendStored = cfg.feed == nil || caps.Journal == nil
	if a.events.feed == nil {
		a.events.feed = newFeed(nil)
		a.events.ownFeed = true
		if caps.Events != nil {
			a.events.feed.setSource(caps.Events)
		}
	}
	a.def.extensions.report = a.reportExtension
	a.life.init()
	a.life.setStatus(SessionStatusCreated)
	go a.loop()
	<-a.life.ready
	return a, nil
}

// Session is a live Session: one conversation running one Agent. Prompt,
// Continue, Steer, Interrupt, FollowUp and Abort drive its Loop; Subscribe
// and SubscribeRun stream its Events; Status and Done report its life; Tools
// and InspectContext show what its next Turn would send.
//
// The Runner builds one from a record.Record when a Run needs it, keeps it
// warm in memory between Runs (host.WarmPolicy), and closes it when it
// leaves memory. The Record stays the truth.
type Session struct {
	// def is what the Agent gave, which nothing changes once the Session runs,
	// so it has no lock.
	def sessionDef
	// life is the Session's life: its commands, the admission of a Run, its
	// status and its close.
	life sessionLife
	// run is the Run in flight.
	run sessionRun
	// events is the Session's stored Events: the Journal, what is staged, the
	// numbers and the Feed.
	events sessionEvents
	// history is the committed history, such as a record.Record, and its size.
	history sessionHistory
	// tools is the Tool registry, with its own lock.
	tools sessionTools
	// queue holds the messages that wait for a Run (Steer, FollowUp,
	// Interrupt, notice); see controlQueue.
	queue *controlQueue
	// host is what the host set through options.
	host sessionHost
}

// A Session is built from parts that each do one job (decision 048): def, life,
// run, events, history, tools, queue and host. Four of them have a lock:
// events.mu, run.mu, the queue's own and tools.mu. The order is events, then
// run, then queue; tools.mu is taken with none of them held and no host call
// is made under any. A part is read and tested alone, and the Session calls
// them in order.

// sessionDef is what the Agent gave the Session: its Model and instruction,
// the Context builder, the extensions, the limits, and the Sampling and cache
// key every model call carries. Nothing changes it after NewSession.
type sessionDef struct {
	id          SessionID
	model       Model
	instruction string
	extensions  extensionSet
	limits      CoreLimits
	// spillPage is the page size of the Session's ReadBack, the line past which
	// a Tool Result is spilled; 0 with none, and then nothing is spilled.
	spillPage int
	// sampling is the Session's own override of the Model's default Sampling, and
	// cacheKey is sent unchanged with every model call.
	sampling       Sampling
	cacheKey       string
	contextBuilder ContextBuilder
}

// sessionHost is what the host asked of the Session through options.
type sessionHost struct {
	// onRunStart is called with the ID of each Run as it starts (withRunStart).
	onRunStart func(RunID)
	// handoff keeps the messages still queued when a Run completes or fails,
	// for the host to take (takeQueued); see withQueueHandoff.
	handoff bool
	// requestID is the caller's request identifier, recorded in RunStarted.
	requestID string
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
func (a *Session) ID() SessionID { return a.def.id }

// Status is where the Session is in its life. It reports
// SessionStatusClosing as soon as Close is requested, until the Session is
// closed.
func (a *Session) Status() SessionStatus {
	status := decodeSessionStatus(a.life.status.Load())
	if a.life.closeRequested.Load() && status != SessionStatusClosed {
		return SessionStatusClosing
	}
	return status
}

// Done returns a channel that is closed once the Session has closed: after
// Close, when no Run is left and the Session has stopped accepting messages.
func (a *Session) Done() <-chan struct{} { return a.life.done }

// Subscribe starts a Subscription to the Session's stored Events and deltas,
// from now on, across Runs. Subscribe before the first Prompt to see
// everything. A subscriber that reads slowly slows only itself: the Session
// never waits for it.
func (a *Session) Subscribe(ctx context.Context, options ...SubscribeOption) (Subscription, error) {
	return a.events.feed.Subscribe(ctx, options...)
}

// Published returns how many Events the Session has published: its stored
// Events and its deltas. A test that reads a Subscription compares it with how
// many it has read, to know that it has seen everything.
func (a *Session) published() uint64 { return a.events.feed.published() }

// SubscribeRun starts a Subscription to the next Run that starts: its Events
// and deltas, from its RunStarted to its RunEnded, with the same numbers the
// Session's stream gives them. Call it before Prompt.
func (a *Session) SubscribeRun(ctx context.Context) (Subscription, error) {
	return a.events.feed.Subscribe(ctx, SubscribeNextRun()...)
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
	// A context that has ended admits nothing: without the check the select
	// below picks at random between sending the command and giving up.
	if err := ctx.Err(); err != nil {
		return RunResult{}, notRunBeforeAdmission(err, operation)
	}

	select {
	case a.life.admission <- struct{}{}:
	default:
		return RunResult{}, runtimeError(CodeBusy, operation, "Session is already processing a Run", nil)
	}
	releaseAdmission := true
	defer func() {
		if releaseAdmission {
			<-a.life.admission
		}
	}()

	cmd := sessionCommand{kind: kind, ctx: ctx, message: message, result: make(chan promptResponse, 1)}
	select {
	case a.life.commands <- cmd:
		releaseAdmission = false
	case <-ctx.Done():
		return RunResult{}, notRunBeforeAdmission(ctx.Err(), operation)
	case <-a.life.closeSignal:
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

// notRunBeforeAdmission is the error of an operation whose ctx ended before
// the Run was admitted: nothing ran and nothing was committed.
func notRunBeforeAdmission(cause error, operation string) *RuntimeError {
	e := runtimeError(codeForContext(cause), operation, operation+" was canceled before admission", cause)
	e.NotRun = true
	return e
}

// settleGrace is how long Prompt and Continue wait, after their ctx ends, for
// the Run to settle under it.
const settleGrace = 5 * time.Second

// Transcript returns the Transcript this Session commits to.
func (a *Session) Transcript() Transcript { return a.history.transcript }

// isDurable reports whether the Session's Events are stored.
func (a *Session) isDurable() (stored bool, failure *RuntimeError) {
	caps, failure := a.history.capabilities()
	return caps.Durable && failure == nil, failure
}
