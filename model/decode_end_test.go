package model

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// decodeAll feeds the data lines of a script to a Decoder as events, then the
// End event, and returns what the Decoder said at the End.
func decodeAll(t *testing.T, decoder Decoder, lines []string) (events []gotato.ModelEvent, done bool, err error) {
	t.Helper()
	for _, line := range lines {
		var got []gotato.ModelEvent
		got, done, err = decoder.Decode(SSEEvent{Data: line})
		events = append(events, got...)
		if err != nil || done {
			return events, done, err
		}
	}
	got, done, err := decoder.Decode(SSEEvent{End: true})
	return append(events, got...), done, err
}

func newDecoderFor(api API) Decoder {
	switch api {
	case APIResponses:
		return newResponsesDecoder(nil)
	case APIAnthropicMessages:
		return newAnthropicDecoder(nil)
	}
	return newChatDecoder(nil)
}

// A chat reply that ends after a finish_reason, with no [DONE], is complete:
// the End event completes it, and the Done event is yielded there.
func TestChatDecodeEndCompletesAfterAFinishReasonWithoutDONE(t *testing.T) {
	lines := textReply[APIChatCompletions]
	events, done, err := decodeAll(t, newChatDecoder(nil), lines[:len(lines)-1]) // no [DONE]
	if err != nil || !done {
		t.Fatalf("done = %v, err = %v", done, err)
	}
	if events[len(events)-1].Kind != gotato.ModelEventKindDone || events[len(events)-1].StopReason != gotato.StopReasonEndTurn || textOf(events) != "ok" {
		t.Fatalf("events = %+v", events)
	}
	// With [DONE] it is complete before the End, and the End is never sent.
	decoder := newChatDecoder(nil)
	events, done, err = decodeAll(t, decoder, lines)
	if err != nil || !done || events[len(events)-1].Kind != gotato.ModelEventKindDone {
		t.Fatalf("with [DONE]: done = %v, err = %v, events = %+v", done, err, events)
	}
}

// Without a finish_reason the chat End says nothing: the reply is cut.
func TestChatDecodeEndWithoutAFinishReasonIsACut(t *testing.T) {
	events, done, err := decodeAll(t, newChatDecoder(nil), textReply[APIChatCompletions][:1])
	if err != nil || done {
		t.Fatalf("done = %v, err = %v", done, err)
	}
	if textOf(events) != "ok" {
		t.Fatalf("events = %+v", events)
	}
	if _, done, err := newChatDecoder(nil).Decode(SSEEvent{End: true}); done || err != nil {
		t.Fatalf("End alone: done = %v, err = %v", done, err)
	}
}

// The Responses and Anthropic replies each have a terminal event of their own;
// the End before it is a cut, and the decoder neither completes nor fails.
func TestOtherWiresTreatEndBeforeTheirTerminalEventAsACut(t *testing.T) {
	for _, api := range []API{APIResponses, APIAnthropicMessages} {
		lines := textReply[api]
		_, done, err := decodeAll(t, newDecoderFor(api), lines[:len(lines)-1])
		if done || err != nil {
			t.Errorf("%s: End before the terminal event: done = %v, err = %v", api, done, err)
		}
		_, done, err = decodeAll(t, newDecoderFor(api), lines)
		if !done || err != nil {
			t.Errorf("%s: the whole reply: done = %v, err = %v", api, done, err)
		}
	}
}

// Through the stream loop: a chat reply with no [DONE] after its finish reason
// completes with no restart.
func TestAChatReplyWithoutDONECompletesWithoutARestart(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writeReply(t, w, textReply[APIChatCompletions][:2])
	}))
	defer server.Close()
	stream, err := chatClientAt(t, server.URL).Stream(context.Background(), gotato.ModelRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	events, err := collectEvents(t, stream)
	if err != nil || textOf(events) != "ok" || events[len(events)-1].Kind != gotato.ModelEventKindDone || calls.Load() != 1 {
		t.Fatalf("text = %q, err = %v, calls = %d", textOf(events), err, calls.Load())
	}
}

// Through the stream loop: the other wires' replies that end before their
// terminal event are cut, and the request is sent again. The first attempt is
// cut before any event reaches the caller, so the restart is transparent.
func TestOtherWiresRestartAReplyThatEndsBeforeItsTerminalEvent(t *testing.T) {
	for _, api := range []API{APIResponses, APIAnthropicMessages} {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if calls.Add(1) == 1 {
				// The body ends between events before anything was sent.
				writeReply(t, w, nil)
				return
			}
			writeReply(t, w, textReply[api])
		}))
		client := newClientFor(t, wireOf(api), server.URL, modelOf(gotato.ModelFacts{}))
		stream, err := client.Stream(context.Background(), hello())
		if err != nil {
			t.Fatal(err)
		}
		events, err := collectEvents(t, stream)
		_ = stream.Close()
		if err != nil || textOf(events) != "ok" || calls.Load() != 2 {
			t.Errorf("%s: text = %q, err = %v, calls = %d", api, textOf(events), err, calls.Load())
		}
		server.Close()
	}
}

// And with output already delivered, with nobody to drop it, the cut is the
// retryable interruption on every wire, worded the same.
func TestEveryWireCutAfterOutputIsTheSameInterruption(t *testing.T) {
	for _, api := range wireAPIs {
		lines := textReply[api]
		var cut []string
		switch api {
		case APIChatCompletions:
			cut = lines[:1]
		case APIResponses:
			cut = lines[:1]
		case APIAnthropicMessages:
			cut = lines[:3]
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { writeReply(t, w, cut) }))
		client := newClientFor(t, wireOf(api), server.URL, modelOf(gotato.ModelFacts{}), WithRetry(Retry{Max: 1, Backoff: time.Millisecond, MaxWait: time.Second}))
		stream, err := client.Stream(context.Background(), hello())
		if err != nil {
			t.Fatal(err)
		}
		events, err := collectEvents(t, stream)
		_ = stream.Close()
		var gerr *Error
		if !errors.As(err, &gerr) || !gerr.Retryable() || gerr.Message != streamInterruptedMessage || !strings.Contains(gerr.Error(), "stream interrupted") {
			t.Errorf("%s: err = %v", api, err)
		}
		if textOf(events) != "ok" {
			t.Errorf("%s: text = %q", api, textOf(events))
		}
		server.Close()
	}
}
