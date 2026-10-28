// Package mcp supplies the tools of an MCP server to a Gotato Session as a
// gotato.ToolSet. One server is one ToolSet; its tools are "<name>.<tool>".
//
// The adapter is a Go module of its own, on the official MCP Go SDK, so a
// service that does not use MCP carries none of it. It builds tools only:
// resources, prompts, sampling, elicitation, OAuth and Gotato as an MCP
// server are not built (FEATURES.md, G-N20). A tool that asks for input in
// the middle of a call gets no answer, and the call ends with a failed Tool
// Result. Progress notifications are the Tool's progress, and the server's
// instructions are the description when WithDescription is not set.
//
// Lifecycle. NewToolSet connects to nothing. The first resolution connects,
// and every Session that installs the same *ToolSet shares that one
// connection (for stdio, one subprocess). A connection that closes, or a
// session that the server forgot (it restarted and answers 404), is dropped
// and made again: a resolution or a call the server answered 404 is sent again
// once on the new session, since the server did not run it, and any other
// failed call is the caller's. The code that made the ToolSet closes it
// with Close, which ends the connection and the subprocess; a Session never
// closes it. A connect is bounded (WithConnectTimeout, 10 seconds by
// default), runs outside the ToolSet's lock, and is shared: callers that
// arrive meanwhile wait for it, each only as long as its own context, and Close
// does not wait for it. A server that accepts the connection and never answers
// makes the ToolSet unavailable for that Run, not for every Session.
//
// The description is the server's instructions as they were at the first
// connect, and stays so across reconnects: it is part of the prompt, and a
// changing one would be a new prefix. It is empty until the first connect;
// WithDescription fixes it from the start. Every call passes the caller's ctx to the SDK, so canceling a
// Run cancels the call.
//
// A server behind a load balancer. A stateful MCP server keeps its sessions in
// the memory of one process, so a request that carries a session ID must reach
// that process. Behind a round-robin balancer with more than one replica, or
// during a rolling deploy while the old and the new process both answer, a
// request can reach a process that does not know the session and gets 404; the
// ToolSet starts a new session and tries once, and what still fails reaches
// the model as a tool error. Run a stateless server, or give the balancer
// session affinity.
package mcp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ToolSetOption configures a ToolSet.
type ToolSetOption func(*config)

type config struct {
	description string
	effects     map[string]gotato.ToolEffect
	cacheTTL    time.Duration
	clientName  string
	clientVer   string
	// connectTimeout bounds one connect (the transport and the initialize
	// exchange); not above zero means no bound.
	connectTimeout time.Duration
}

// DefaultConnectTimeout is the bound of one connect when WithConnectTimeout
// is not given.
const DefaultConnectTimeout = 10 * time.Second

// WithConnectTimeout bounds one connect to the server: the transport and the
// initialize exchange. A server that accepts the connection and never answers
// ends the connect with an error after d, and the ToolSet is unavailable for
// that Run. The default is DefaultConnectTimeout; zero or less means no bound
// (the caller's context still ends it).
func WithConnectTimeout(d time.Duration) ToolSetOption {
	return func(c *config) { c.connectTimeout = d }
}

// WithDescription sets the ToolSet's description, which the catalog shows.
func WithDescription(text string) ToolSetOption {
	return func(c *config) { c.description = text }
}

// WithToolEffect sets the Effect of one tool (by its name on the server),
// replacing the one its annotations give.
func WithToolEffect(tool string, effect gotato.ToolEffect) ToolSetOption {
	return func(c *config) { c.effects[tool] = effect }
}

// WithListCache keeps a tool list for ttl, so a Run that resolves the ToolSet
// does not ask the server for it again unless the server sent list_changed.
// The default 0 asks the server at every resolution (one request per Run),
// which sees a change even when the server cannot notify.
func WithListCache(ttl time.Duration) ToolSetOption {
	return func(c *config) { c.cacheTTL = ttl }
}

// WithClientInfo sets the name and version this client tells the server.
func WithClientInfo(name, version string) ToolSetOption {
	return func(c *config) { c.clientName, c.clientVer = name, version }
}

// ToolSet is an MCP server as a gotato.ToolSet. Make one with NewToolSet and
// install it with gotato.WithToolSet or WithFoldedToolSet. It is safe for
// concurrent use.
type ToolSet struct {
	name      string
	cfg       config
	transport Transport
	client    *sdk.Client
	// changed is set by the server's list_changed and cleared by a listing.
	changed atomic.Bool
	// progress maps the progress token of a call in flight to the Tool's
	// progress function; tokens counts them.
	progress sync.Map
	tokens   atomic.Uint64
	// instructions is what the server said at initialize, kept after the
	// connection is dropped.
	instructions atomic.Pointer[string]

	connMu  sync.Mutex
	session *sdk.ClientSession
	closed  bool
	// connecting is the connect in flight, shared by the callers that wait for
	// it; nil when none is.
	connecting *connectAttempt

	cacheMu  sync.Mutex
	cached   []gotato.Tool
	cachedAt time.Time
	haveList bool
}

var _ gotato.ToolSet = (*ToolSet)(nil)

// NewToolSet makes the ToolSet of one MCP server. It does not connect: the
// first resolution does, so a server that is down makes the ToolSet
// unavailable for a Run (the Session still starts) and not an error here.
func NewToolSet(name string, transport Transport, options ...ToolSetOption) (*ToolSet, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.Contains(name, ".") {
		return nil, fmt.Errorf("mcp: ToolSet name %q must be non-empty and contain no dot", name)
	}
	if transport == nil {
		return nil, errors.New("mcp: transport is nil")
	}
	s := &ToolSet{name: name, transport: transport, cfg: config{
		effects: map[string]gotato.ToolEffect{}, clientName: "gotato", clientVer: "0",
		connectTimeout: DefaultConnectTimeout,
	}}
	for _, option := range options {
		if option != nil {
			option(&s.cfg)
		}
	}
	for tool, effect := range s.cfg.effects {
		if !effect.Valid() {
			return nil, fmt.Errorf("mcp: unknown Effect %q for tool %q", effect, tool)
		}
	}
	s.client = sdk.NewClient(&sdk.Implementation{Name: s.cfg.clientName, Version: s.cfg.clientVer}, &sdk.ClientOptions{
		ToolListChangedHandler: func(context.Context, *sdk.ToolListChangedRequest) { s.changed.Store(true) },
		// A server that asks for input in the middle of a call gets no answer:
		// there is no user and no model to ask. The SDK's automatic answer is
		// off, and the call ends with a failed Tool Result (see Execute).
		MultiRoundTrip:              &sdk.MultiRoundTripOptions{Disabled: true},
		ProgressNotificationHandler: s.forwardProgress,
	})
	return s, nil
}

// Spec implements gotato.ToolSet.
func (s *ToolSet) Spec() gotato.ToolSetSpec {
	description := s.cfg.description
	if description == "" {
		if text := s.instructions.Load(); text != nil {
			description = firstParagraph(*text)
		}
	}
	return gotato.ToolSetSpec{Name: s.name, Description: description}
}

// firstParagraph is the first paragraph of a server's instructions on one
// line: the catalog shows a description as one line, and what follows the
// first paragraph is for a model that already uses the tools.
func firstParagraph(text string) string {
	text = strings.ReplaceAll(strings.TrimSpace(text), "\r\n", "\n")
	first, _, _ := strings.Cut(text, "\n\n")
	return strings.Join(strings.Fields(first), " ")
}

// forwardProgress hands a progress notification to the call it belongs to.
func (s *ToolSet) forwardProgress(_ context.Context, req *sdk.ProgressNotificationClientRequest) {
	if req == nil || req.Params == nil {
		return
	}
	token, ok := req.Params.ProgressToken.(string)
	if !ok {
		return
	}
	report, ok := s.progress.Load(token)
	if !ok {
		return
	}
	text := strings.TrimSpace(req.Params.Message)
	if text == "" {
		text = fmt.Sprintf("%g", req.Params.Progress)
		if req.Params.Total > 0 {
			text += fmt.Sprintf("/%g", req.Params.Total)
		}
	}
	report.(gotato.ToolProgressFunc)(text)
}

// Tools implements gotato.ToolSet: it lists the server's tools. A failure
// leaves the ToolSet unavailable for the Run that asked.
func (s *ToolSet) Tools(ctx context.Context) ([]gotato.Tool, error) {
	if tools, ok := s.fromCache(); ok {
		return tools, nil
	}
	s.changed.Store(false) // before listing, so a change during it is not lost
	listed, err := s.list(ctx)
	if err != nil {
		return nil, err
	}
	tools := make([]gotato.Tool, 0, len(listed))
	for _, t := range listed {
		if t.Name == "" {
			continue
		}
		tools = append(tools, newTool(s, t))
	}
	s.cacheMu.Lock()
	s.cached, s.cachedAt, s.haveList = tools, time.Now(), true
	s.cacheMu.Unlock()
	return slices.Clone(tools), nil
}

func (s *ToolSet) fromCache() ([]gotato.Tool, bool) {
	if s.cfg.cacheTTL <= 0 || s.changed.Load() {
		return nil, false
	}
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if !s.haveList || time.Since(s.cachedAt) >= s.cfg.cacheTTL {
		return nil, false
	}
	return slices.Clone(s.cached), true
}

// list reads every page of the server's tools. A closed connection, or a
// session the server forgot, is made again once, since listing changes
// nothing.
func (s *ToolSet) list(ctx context.Context) ([]*sdk.Tool, error) {
	for attempt := 0; ; attempt++ {
		session, err := s.connect(ctx)
		if err != nil {
			return nil, err
		}
		var tools []*sdk.Tool
		var listErr error
		for tool, err := range session.Tools(ctx, nil) {
			if err != nil {
				listErr = err
				break
			}
			tools = append(tools, tool)
		}
		if listErr == nil {
			return tools, nil
		}
		s.drop(session, listErr)
		if attempt > 0 || !gone(listErr) || ctx.Err() != nil {
			return nil, fmt.Errorf("mcp %s: list tools: %w", s.name, listErr)
		}
	}
}

// connectAttempt is one connect to the server. The caller that started it
// makes it; the others wait on done, each bounded by its own context.
type connectAttempt struct {
	done     chan struct{}
	session  *sdk.ClientSession
	err      error
	timedOut bool
}

// connect returns the shared session, making it when there is none. The
// network connect runs outside the lock, bounded by the connect timeout, so a
// server that never answers holds neither another caller (each waits only as
// long as its own context) nor Close.
func (s *ToolSet) connect(ctx context.Context) (*sdk.ClientSession, error) {
	for {
		s.connMu.Lock()
		if s.closed {
			s.connMu.Unlock()
			return nil, fmt.Errorf("mcp %s: ToolSet is closed", s.name)
		}
		if s.session != nil {
			session := s.session
			s.connMu.Unlock()
			return session, nil
		}
		if attempt := s.connecting; attempt != nil {
			s.connMu.Unlock()
			select {
			case <-attempt.done:
			case <-ctx.Done():
				return nil, fmt.Errorf("mcp %s: connect: %w", s.name, ctx.Err())
			}
			if attempt.err == nil {
				return attempt.session, nil
			}
			// The attempt failed on its starter's context, not on the server:
			// try again on this caller's own.
			if ctx.Err() == nil && (errors.Is(attempt.err, context.Canceled) || errors.Is(attempt.err, context.DeadlineExceeded)) && !attempt.timedOut {
				continue
			}
			return nil, attempt.err
		}
		attempt := &connectAttempt{done: make(chan struct{})}
		s.connecting = attempt
		s.connMu.Unlock()

		attempt.session, attempt.timedOut, attempt.err = s.dial(ctx)

		s.connMu.Lock()
		s.connecting = nil
		if attempt.err == nil && s.closed {
			// Close came while connecting: nothing keeps this session.
			_ = attempt.session.Close()
			attempt.session, attempt.err = nil, fmt.Errorf("mcp %s: ToolSet is closed", s.name)
		}
		if attempt.err == nil {
			s.session = attempt.session
		}
		s.connMu.Unlock()
		close(attempt.done)
		return attempt.session, attempt.err
	}
}

// dial makes one connection, bounded by the connect timeout. timedOut says the
// bound ended it, which is the server's doing and not the caller's.
func (s *ToolSet) dial(ctx context.Context) (session *sdk.ClientSession, timedOut bool, err error) {
	dialCtx := ctx
	if s.cfg.connectTimeout > 0 {
		var cancel context.CancelFunc
		dialCtx, cancel = context.WithTimeout(ctx, s.cfg.connectTimeout)
		defer cancel()
	}
	// The SDK does not always end a connect when its context ends (a streamable
	// HTTP transport's request has a context of its own), so the connect runs
	// on its own goroutine and the wait ends with dialCtx. A connect that
	// finishes after that is closed.
	type dialed struct {
		session *sdk.ClientSession
		err     error
	}
	out := make(chan dialed, 1)
	go func() {
		session, err := s.client.Connect(dialCtx, s.transport(), nil)
		out <- dialed{session, err}
	}()
	select {
	case got := <-out:
		session, err = got.session, got.err
	case <-dialCtx.Done():
		go func() {
			if got := <-out; got.session != nil {
				_ = got.session.Close()
			}
		}()
		err = dialCtx.Err()
	}
	if err != nil {
		timedOut = ctx.Err() == nil && dialCtx.Err() != nil
		return nil, timedOut, fmt.Errorf("mcp %s: connect: %w", s.name, err)
	}
	if init := session.InitializeResult(); init != nil {
		// The first connect's instructions are kept: the description is part of
		// the prompt, and a server that restarts with other words must not
		// change it between two Turns.
		instructions := init.Instructions
		s.instructions.CompareAndSwap(nil, &instructions)
	}
	return session, false, nil
}

// gone reports whether err says the session cannot be used again: its
// connection closed, or the server answered 404 to its ID, as a restarted
// server does (MCP §2.5.3: the client then starts a new session).
func gone(err error) bool {
	return errors.Is(err, sdk.ErrConnectionClosed) || errors.Is(err, sdk.ErrSessionMissing)
}

// drop forgets a session whose connection is gone, so the next call connects
// again.
func (s *ToolSet) drop(session *sdk.ClientSession, cause error) {
	if !gone(cause) {
		return
	}
	s.connMu.Lock()
	if s.session == session {
		s.session = nil
	}
	s.connMu.Unlock()
	_ = session.Close()
}

// Close ends the connection, and the subprocess of a stdio transport. The
// ToolSet is unusable afterwards. Close it when the service shuts down; a
// Session never does.
func (s *ToolSet) Close() error {
	s.connMu.Lock()
	s.closed = true
	session := s.session
	s.session = nil
	s.connMu.Unlock()
	if session == nil {
		return nil
	}
	return session.Close()
}
