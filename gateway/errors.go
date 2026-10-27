package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type Error struct {
	StatusCode int
	retryable  bool
	Message    string
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
