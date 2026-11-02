package gateway

import (
	"context"
	"fmt"
	"maps"
	"net/url"
	"strings"
)

// defaultUserAgent identifies Gotato when neither the Connection nor the
// request context names a client. Providers that require callers to identify
// themselves (for example with their own product name) are satisfied by a
// User-Agent entry in Connection.Headers or WithRequestHeaders.
const defaultUserAgent = "gotato/0.1"

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
// value winning. They are applied after Connection.Headers and before the
// wire's own headers, so they can override configuration but never
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
	maps.Copy(merged, headers)
	return context.WithValue(ctx, requestHeadersKey{}, merged)
}

// RequestHeaders returns a copy of the per-request headers attached to ctx, or
// nil. Writing to it changes nothing in ctx.
func RequestHeaders(ctx context.Context) map[string]string {
	if ctx == nil {
		return nil
	}
	headers, _ := ctx.Value(requestHeadersKey{}).(map[string]string)
	return maps.Clone(headers)
}

// withAnthropicBeta adds a beta to a comma-separated anthropic-beta list, after
// any betas the Connection or the request context already names.
func withAnthropicBeta(current, beta string) string {
	for name := range strings.SplitSeq(current, ",") {
		if strings.TrimSpace(name) == beta {
			return current
		}
	}
	if current != "" {
		return current + "," + beta
	}
	return beta
}

// validHeaderName reports whether name is an HTTP header field name: one or
// more token characters (RFC 9110).
func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			continue
		}
		if !strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c)) {
			return false
		}
	}
	return true
}

// validHeaderValue reports whether value has no control character but a tab.
func validHeaderValue(value string) bool {
	for i := 0; i < len(value); i++ {
		if c := value[i]; c < ' ' && c != '\t' || c == 0x7f {
			return false
		}
	}
	return true
}

// [CONTRACT] net/http refuses a bad header only at the first request; the package promises a settings mistake is reported before it.
// checkHeaders returns an error naming the first header that net/http would
// refuse to send. The value is never part of the error.
func checkHeaders(headers map[string]string) error {
	for name, value := range headers {
		if !validHeaderName(name) {
			return fmt.Errorf("%q is not a header name", name)
		}
		if !validHeaderValue(value) {
			return fmt.Errorf("the value of %q has a control character", name)
		}
	}
	return nil
}

// [CONTRACT] a settings mistake is reported by New, before the first Stream.
// checkEndpoint refuses an endpoint that is not an http or https URL with a host.
func checkEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("gateway: the endpoint is not an http or https URL with a host")
	}
	return nil
}
