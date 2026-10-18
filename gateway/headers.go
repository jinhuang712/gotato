package gateway

import (
	"context"
	"maps"
	"net/http"
	"strings"
)

// DefaultUserAgent identifies Gotato when neither the configuration nor the
// request context names a client. Providers that require callers to identify
// themselves (for example with their own product name) are satisfied by a
// User-Agent entry in Config.Headers or WithRequestHeaders.
const DefaultUserAgent = "gotato/0.1"

type requestHeadersKey struct{}

// WithRequestHeaders returns a context whose Model requests carry extra HTTP
// headers. It is how a host attaches per-conversation metadata that a static
// configuration cannot express, such as a provider's session-routing header
// with one stable value per Session:
//
//	ctx = gateway.WithRequestHeaders(ctx, map[string]string{"x-session": sessionID})
//	session.Prompt(ctx, message)
//
// A Session passes its Run context to Model.Stream, so the headers reach
// every request of that Run. Headers from nested calls merge, the innermost
// value winning. They are applied after Config.Headers and before the
// adapter's protocol headers, so they can override configuration but never
// authentication, content type, or stream negotiation.
func WithRequestHeaders(ctx context.Context, headers map[string]string) context.Context {
	if len(headers) == 0 {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	merged := maps.Clone(RequestHeaders(ctx))
	if merged == nil {
		merged = make(map[string]string, len(headers))
	}
	for key, value := range headers {
		merged[key] = value
	}
	return context.WithValue(ctx, requestHeadersKey{}, merged)
}

// RequestHeaders returns the per-request headers attached to ctx, or nil.
func RequestHeaders(ctx context.Context) map[string]string {
	if ctx == nil {
		return nil
	}
	headers, _ := ctx.Value(requestHeadersKey{}).(map[string]string)
	return headers
}

// addAnthropicBeta adds a beta to the anthropic-beta header, after any
// betas the configuration or the request context already names.
func addAnthropicBeta(header http.Header, beta string) {
	current := header.Get("anthropic-beta")
	for _, name := range strings.Split(current, ",") {
		if strings.TrimSpace(name) == beta {
			return
		}
	}
	if current != "" {
		beta = current + "," + beta
	}
	header.Set("anthropic-beta", beta)
}

// applyHeaders sets the default User-Agent, then configured headers, then
// per-request headers. Callers set protocol headers afterwards.
func (c *Client) applyHeaders(ctx context.Context, req *http.Request) {
	req.Header.Set("User-Agent", DefaultUserAgent)
	for key, value := range c.headers {
		req.Header.Set(key, value)
	}
	for key, value := range RequestHeaders(ctx) {
		req.Header.Set(key, value)
	}
}
