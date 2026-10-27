package session_test

import (
	"context"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
)

// journaled is what a Record and a Writer both are: a Transcript that keeps the
// Events a live Session writes.
type journaled interface {
	gotato.Transcript
	gotato.Journal
}

func header() gotato.Header { return gotato.NewHeader("", "", 0) }

// messageEvent is the Event that carries a Message into the Transcript.
func messageEvent(message gotato.Message) gotato.Event {
	switch {
	case message.Role == gotato.RoleUser:
		return gotato.MessageCommitted{Header: header(), Message: message, Source: gotato.MessageSourcePrompt}
	case message.Role == gotato.RoleToolResult && message.ToolResult != nil:
		return gotato.ToolResultCommitted{Header: header(), Message: message, CallID: message.ToolResult.CallID, Status: message.ToolResult.Status}
	default:
		return gotato.ReplyEnded{Header: header(), Message: message, StopReason: message.StopReason, ToolCalls: len(message.ToolCalls)}
	}
}

// commit puts a Message in the Transcript and writes the Event that stores it,
// as a live Session does, and returns the numbered Events written.
func commit(t *testing.T, j journaled, message gotato.Message) []gotato.Event {
	t.Helper()
	if err := j.Append(message); err != nil {
		t.Fatal(err)
	}
	events, err := j.Write(context.Background(), []gotato.Event{messageEvent(message)})
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func kindsOf(events []gotato.Event) []gotato.EventKind {
	out := make([]gotato.EventKind, len(events))
	for i, event := range events {
		out[i] = event.Kind()
	}
	return out
}
