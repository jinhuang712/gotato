package gateway

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/jinhuang712/gotato/gotato"
)

// Error is a provider's refusal or a failed request. StatusCode is the HTTP
// status, or 0 when the failure was not one (a transport error, a stream cut
// off). Message is the provider's own text where it sent one.
type Error struct {
	StatusCode int
	retryable  bool
	Message    string
	// cause is the error under a transport failure, kept for errors.Is and
	// errors.As; Message is the text callers show.
	cause error
	// Overflow is set when the provider refused the request, with a 400 or
	// 413, because it was longer than the model's context window.
	Overflow bool
}

// ContextOverflow reports whether the request was refused for being too long.
// The runtime reads it by this method, so it imports nothing from here, and
// ends the Run with the code context_overflow instead of model_failure.
func (e *Error) ContextOverflow() bool { return e != nil && e.Overflow }

// ContextLimit returns the limit, in tokens, that a refusal as too long names
// in its message, such as the "200000" of "prompt is too long: 210000 tokens >
// 200000 maximum", or 0 when it names none. Compaction reads it, by this
// method, to size its merge of the oldest Segments.
func (e *Error) ContextLimit() int {
	if e == nil || !e.Overflow {
		return 0
	}
	return limitIn(e.Message)
}

// Retryable reports whether the same request may succeed when sent again
// (a rate limit, an overload, a broken stream). The runtime reads it by this
// method, and sets Retryable on the Run's model_failure.
func (e *Error) Retryable() bool { return e != nil && e.retryable }

// Unwrap returns the transport error under this one, or nil.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// Error implements the error interface: "gateway: HTTP 429: message", or
// without the status when there was none.
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.StatusCode == 0 {
		return "gateway: " + e.Message
	}
	return fmt.Sprintf("gateway: HTTP %d: %s", e.StatusCode, e.Message)
}

// errorBody is what a refused request's body says: the message to show, and
// the code and type the provider named, either of which may carry the reason
// alone.
type errorBody struct {
	Message, Code, Type string
}

func readErrorBody(body io.Reader) errorBody {
	data, _ := io.ReadAll(io.LimitReader(body, 64<<10))
	if len(data) == 0 {
		return errorBody{Message: "empty response"}
	}
	var structured struct {
		Error struct {
			Message string          `json:"message"`
			Code    json.RawMessage `json:"code"`
			Type    string          `json:"type"`
		} `json:"error"`
	}
	out := errorBody{}
	if json.Unmarshal(data, &structured) == nil {
		out = errorBody{Message: structured.Error.Message, Code: errorCode(structured.Error.Code), Type: structured.Error.Type}
	}
	if out.Message == "" {
		out.Message = strings.TrimSpace(string(data))
		if len(out.Message) > 512 {
			out.Message = out.Message[:512]
		}
	}
	return out
}

// errorCode reads an error's code, which providers write as a string, as a
// number or as null.
func errorCode(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	if string(raw) == "null" {
		return ""
	}
	return string(raw)
}

// transientStreamKinds are the types and codes by which a provider, in an
// error it sends in the middle of a stream, says the failure is its own and
// may pass: Anthropic's overloaded_error, api_error and rate_limit_error,
// and the server_error and rate_limit_exceeded of the OpenAI wires. They
// are the one table of all three decoders. A stream that started is never
// sent again by the gateway: the error is marked Retryable for the caller,
// which can run the Turn again.
var transientStreamKinds = map[string]bool{
	"overloaded_error":          true,
	"api_error":                 true,
	"rate_limit_error":          true,
	"server_error":              true,
	"rate_limit_exceeded":       true,
	"service_unavailable_error": true,
}

// transientStream reports whether any of the types and codes of a stream
// error says the failure may pass.
func transientStream(kinds ...string) bool {
	for _, kind := range kinds {
		if transientStreamKinds[strings.ToLower(strings.TrimSpace(kind))] {
			return true
		}
	}
	return false
}

// transientTransport reports whether a failed send may succeed when sent
// again. A certificate the client does not trust or that does not fit the
// host, and a host name that does not exist, are mistakes in the settings
// and stay the same on every attempt.
func transientTransport(err error) bool {
	var (
		unknownAuthority x509.UnknownAuthorityError
		invalid          x509.CertificateInvalidError
		hostname         x509.HostnameError
		verification     *tls.CertificateVerificationError
		dns              *net.DNSError
		invalidAddr      net.InvalidAddrError
		unknownNetwork   net.UnknownNetworkError
	)
	switch {
	case errors.As(err, &unknownAuthority), errors.As(err, &invalid), errors.As(err, &hostname),
		errors.As(err, &verification), errors.As(err, &invalidAddr), errors.As(err, &unknownNetwork):
		return false
	case errors.As(err, &dns):
		return !dns.IsNotFound
	}
	return true
}

// protocolFailure is the error of a reply that breaks the wire API's rules:
// the runtime ends the Run with the code model_protocol_failure.
func protocolFailure(format string, args ...any) error {
	return &gotato.RuntimeError{Code: gotato.CodeModelProtocolFailure, Operation: "ModelStream", Message: "gateway: " + fmt.Sprintf(format, args...)}
}
