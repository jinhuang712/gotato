package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Transport makes a fresh SDK transport each time the ToolSet connects, since
// a transport (a subprocess, say) cannot be used twice.
type Transport func() sdk.Transport

// HTTPOption configures StreamableHTTP.
type HTTPOption func(*httpConfig)

type httpConfig struct {
	client         *http.Client
	noNotification bool
}

// WithHTTPClient sets the HTTP client of a streamable HTTP transport, for
// timeouts, a proxy, or a RoundTripper that adds an Authorization header. Do not set
// http.Client.Timeout: it cuts the standalone SSE stream. Use
// Transport.ResponseHeaderTimeout and the Run's context instead.
func WithHTTPClient(client *http.Client) HTTPOption {
	return func(c *httpConfig) { c.client = client }
}

// WithoutNotifications keeps the client from opening the standalone stream
// that carries a server's list_changed. The ToolSet then relies on its list
// cache (WithListCache) alone to see a change, and on each Run when that is 0.
func WithoutNotifications() HTTPOption {
	return func(c *httpConfig) { c.noNotification = true }
}

// StreamableHTTP connects to an MCP endpoint over streamable HTTP. Gotato uses
// it statelessly: no state of the server is kept for a Gotato Session, so any
// Instance can make any call.
func StreamableHTTP(endpoint string, options ...HTTPOption) Transport {
	var cfg httpConfig
	for _, option := range options {
		option(&cfg)
	}
	return func() sdk.Transport {
		return &sdk.StreamableClientTransport{
			Endpoint:             endpoint,
			HTTPClient:           cfg.client,
			DisableStandaloneSSE: cfg.noNotification,
		}
	}
}

// StdioCommand starts a command and speaks MCP over its stdin and stdout. The
// subprocess belongs to the ToolSet and is shared by its Sessions. newCmd is
// called at every connect and must return a new *exec.Cmd, which also sets the
// command's environment and working directory:
//
//	mcp.StdioCommand(func() *exec.Cmd { return exec.Command("server", "--stdio") })
func StdioCommand(newCmd func() *exec.Cmd) Transport {
	return func() sdk.Transport { return &sdk.CommandTransport{Command: newCmd()} }
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
			session, retry, err := s.waitConnect(ctx, attempt)
			if retry {
				continue
			}
			return session, err
		}
		attempt := &connectAttempt{done: make(chan struct{})}
		s.connecting = attempt
		s.connMu.Unlock()
		return s.makeConnect(ctx, attempt)
	}
}

// waitConnect waits for the connect another caller started, as long as ctx
// allows. retry says the attempt failed on its starter's context, so this
// caller should connect again on its own.
func (s *ToolSet) waitConnect(ctx context.Context, attempt *connectAttempt) (session *sdk.ClientSession, retry bool, err error) {
	select {
	case <-attempt.done:
	case <-ctx.Done():
		return nil, false, fmt.Errorf("mcp %s: connect: %w", s.name, ctx.Err())
	}
	if attempt.err == nil {
		return attempt.session, false, nil
	}
	// The attempt failed on its starter's context, not on the server:
	// try again on this caller's own.
	if ctx.Err() == nil && (errors.Is(attempt.err, context.Canceled) || errors.Is(attempt.err, context.DeadlineExceeded)) && !attempt.timedOut {
		return nil, true, nil
	}
	return nil, false, attempt.err
}

// makeConnect makes the connect this caller started, outside the lock, then
// keeps its session under the lock and wakes the callers that wait for it.
func (s *ToolSet) makeConnect(ctx context.Context, attempt *connectAttempt) (*sdk.ClientSession, error) {
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
		s.gen.Add(1)
	}
	s.connMu.Unlock()
	close(attempt.done)
	return attempt.session, attempt.err
}
