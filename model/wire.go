package model

import (
	"net/http"

	"github.com/jinhuang712/gotato/gotato"
)

// WireAPI is the first layer of a Client: how a request and its reply are
// written on the wire. It holds the shape of one protocol and nothing else: a
// wire never sends, retries or classifies, and nothing under it reads its
// bytes, so the rule that a request only grows at its end (DESIGNS.md,
// principle 2) is the wire's own to keep.
//
// ChatCompletions, Responses and AnthropicMessages are the shipped wires. A
// new protocol is one type outside Gotato, passed to New like these; it
// reuses the Connection, the Retry and the Classifier as they are.
//
// A wire that also has a method Validate() error is checked by New once, so a
// mistake in its settings is refused before the first call, not at it.
type WireAPI interface {
	// Encode writes the request of one call: the body, the headers the wire
	// itself sets, and the Decoder of its reply. It is called once per Stream,
	// and the Request it returns is sent again as it is when the transport
	// retries or the stream restarts.
	Encode(Call) (*Request, error)
}

// Call is one model call as Encode sees it.
type Call struct {
	// Model is what the Client was built with: the name to send and the facts
	// that steer the wire (the cache retention, the reasoning rules).
	Model gotato.ModelConfig
	// Request is the Context to send, with the Sampling of this call.
	Request gotato.ModelRequest
	// APIKey is the Connection's key, for the wire to put where its
	// protocol wants a credential. Empty when there is none.
	APIKey string
	// CacheKey is Request.CacheKey, which the wire may put in the body (the
	// Connection's CacheKeyHeader puts it in a header whatever the wire).
	CacheKey string
	// Header is the headers the Connection and the request's context give
	// this call, before the wire's own are applied. It is for a wire that adds
	// to one of them (a list of beta flags) instead of replacing it, and must
	// not be changed.
	Header http.Header
}

// Request is what a wire encoded: one HTTP request, and how to read its reply.
type Request struct {
	// Body is the request body, a JSON object for the shipped wires.
	Body []byte
	// Header holds the headers the wire sets: its credential and its protocol
	// headers. They win over the Connection's own, so no configured header
	// can take away the credential. Content-Type and Accept are set by the
	// Client for every wire.
	Header http.Header
	// NewDecoder returns a Decoder for one attempt. The Client calls it again
	// when it sends the request again, so a Decoder keeps its state for one
	// reply only.
	NewDecoder func() Decoder
}

// Decoder turns the events of one reply into ModelEvents. It is never called
// from two goroutines.
type Decoder interface {
	// Decode reads one Server-Sent Event of the reply. It returns the
	// ModelEvents it yields, in order, and whether the reply is complete: after
	// done the Client reads no more. An error ends the reply after the
	// events returned with it. A Failure (the error an event of the stream
	// carries) is classified by the Client's Classifier; any other error ends
	// the Run as it is, as a protocol failure does.
	//
	// When the body ends between events, Decode is called once with an event
	// whose End is set. A Decoder that can tell the reply was complete
	// answers done, as a chat reply does after a finish reason without a
	// closing [DONE]; one that cannot answers nothing, and the Client treats
	// the reply as cut off and sends the request again.
	Decode(SSEEvent) (events []gotato.ModelEvent, done bool, err error)
}

// SSEEvent is one Server-Sent Event: the lines up to a blank line, with the
// event name (an "event:" line, empty when there is none) and the data lines
// joined by a newline.
type SSEEvent struct {
	Name string
	Data string
	// End is set on the one event the Client makes when the body ends between
	// events; Name and Data are then empty.
	End bool
}
