package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/text"
)

// Error is a provider's refusal or a failed request. StatusCode is the HTTP
// status, or 0 when the failure was not one (a transport error, an error body sent with a 2xx status, a stream cut
// off). Message is the provider's own text where it sent one.
type Error struct {
	StatusCode int
	retryable  bool
	Message    string
	// cause is the error under a transport failure, kept for errors.Is and
	// errors.As; Message is the text callers show.
	cause error
	// Overflow is set when the request was refused for being longer than the
	// model's context window, as the Classifier says (DefaultClassifier: a 400
	// or 413 that says so, or an error event that does).
	Overflow bool
	// limit is the Verdict's Limit.
	limit int
}

// newError is the Error a Failure becomes under the Classifier's Verdict.
func newError(failure Failure, verdict Verdict) *Error {
	return &Error{StatusCode: failure.Status, retryable: verdict.Retryable, Message: failure.Message, cause: failure.Err, Overflow: verdict.Overflow, limit: verdict.Limit}
}

// ContextOverflow reports whether the request was refused for being too long.
// The runtime reads it by this method, so it imports nothing from here, and
// ends the Run with the code context_overflow instead of model_failure.
func (e *Error) ContextOverflow() bool { return e != nil && e.Overflow }

// ContextLimit returns the limit, in tokens, that a refusal as too long names
// (the Verdict's Limit), such as the "200000" of "prompt is too long: 210000
// tokens > 200000 maximum", or 0 when it names none. Compaction reads it, by
// this method, to size its merge of the oldest Segments.
func (e *Error) ContextLimit() int {
	if e == nil || !e.Overflow {
		return 0
	}
	return e.limit
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

// Public returns the error's text for the Model, Events and clients. A Message
// is the provider's own reply (cut on a rune boundary) or a fixed text of the
// gateway; the transport error that may name a host stays on Unwrap. So a
// caller sees why a provider refused ("model not found"), never where the
// request went.
//
// [SECURITY] the transport cause that may name a host stays on Unwrap and is never public.
func (e *Error) Public() string { return e.Error() }

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
		out.Message = out.Message[:text.Cut(out.Message, 512)]
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

// [SECURITY] a fixed text, so the transport error that names a host never reaches the Model or a client.
// transportMessage is the public Message of a request that could not be sent.
const transportMessage = "the request could not be sent"

// protocolFailure is the error of a reply that breaks the wire API's rules:
// the runtime ends the Run with the code model_protocol_failure.
func protocolFailure(format string, args ...any) error {
	return &gotato.RuntimeError{Code: gotato.CodeModelProtocolFailure, Operation: "ModelStream", Message: "gateway: " + fmt.Sprintf(format, args...)}
}
