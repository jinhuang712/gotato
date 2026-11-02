package gateway

import (
	"net"
	"net/http"
	"time"
)

// Connection is the second layer of a Client: how to reach the model and as
// whom, whatever wire is spoken over it. Only the Endpoint is required.
type Connection struct {
	// Endpoint is the complete URL requests are posted to, with its path.
	// Gotato keeps no table of providers' addresses or paths.
	Endpoint string
	// APIKey is the credential, handed to the wire in Call.APIKey to put where
	// its protocol wants one. Empty sends none, which keyless local servers
	// rely on. A credential that is not a static key (a signed request, a
	// token that expires) goes in the RoundTripper of HTTPClient, as for MCP.
	APIKey string
	// CacheKeyHeader, when set, sends each request's cache key in this HTTP
	// header, for a provider that routes its prompt cache by a session header
	// such as "session-id". A wire may also put the key in the body.
	CacheKeyHeader string
	// Headers are sent with every request. They are applied under the wire's
	// own headers, so they cannot replace the credential, the content type or
	// the stream negotiation. User-Agent defaults to gotato/0.1.
	Headers map[string]string
	// HTTPClient sends the requests. Nil is NewHTTPClient(10s, 60s). A client
	// you pass is used as it is and keeps its own timeouts, proxy and
	// transport.
	HTTPClient *http.Client
}

// The timeouts of the HTTP client the gateway makes when Connection.HTTPClient
// is nil.
const (
	defaultConnectTimeout        = 10 * time.Second
	defaultResponseHeaderTimeout = 60 * time.Second
)

// NewHTTPClient returns an HTTP client with timeouts on it, so a stalled
// upstream cannot hold a Run open forever: connect bounds opening a
// connection (the dial and the TLS handshake, each), and header bounds the
// wait for the response headers after the request is sent. A model behind a
// proxy that sends headers only with its first token may need more. A zero
// duration sets no bound. The stream that follows the headers is bounded by
// the caller's context. The client is what a Connection with no HTTPClient
// uses, with 10 seconds and 60 seconds.
func NewHTTPClient(connect, header time.Duration) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: connect, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   connect,
			ResponseHeaderTimeout: header,
			ExpectContinueTimeout: time.Second,
			IdleConnTimeout:       90 * time.Second,
		},
	}
}
