package mcp

import (
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
// timeouts, a proxy, or a RoundTripper that adds an Authorization header.
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
