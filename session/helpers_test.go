package session_test

import (
	"context"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
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

func storedKinds(t *testing.T, store session.Store, id string, after uint64) []gotato.EventKind {
	t.Helper()
	events, err := session.StoredEvents(context.Background(), store, id, after, 0)
	if err != nil {
		t.Fatal(err)
	}
	return kindsOf(events)
}

func runSession(t *testing.T, s *session.Record, model gotato.Model, prompt string) gotato.RunResult {
	t.Helper()
	agent, err := gotato.NewSession(
		context.Background(),
		gotato.WithModel(model),
		gotato.WithTranscript(s),
		gotato.WithTools(testkit.DemoEchoTool()),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	result, err := agent.Prompt(context.Background(), gotato.UserMessage(prompt))
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func fileEntry(seq uint64, kind gotato.EventKind) session.Entry {
	return session.Entry{Seq: seq, Kind: kind, Layer: session.LayerFact, Data: []byte(`{}`)}
}

func fileSummary(seq uint64) session.RecordInfo {
	at := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	return session.RecordInfo{ID: "r", RecordEpoch: "e1", CreatedAt: at, UpdatedAt: at, Seq: seq}
}

func idMessage(id, role, text string) gotato.Message {
	message := gotato.UserMessage(text)
	if role == "assistant" {
		message = gotato.AssistantMessage(text)
	}
	message.ID = gotato.MessageID(id)
	return message
}

func rendered(number int, texts ...string) gotato.Segment {
	segment := gotato.Segment{Number: number, From: time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC), To: time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)}
	for i, text := range texts {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		segment.Messages = append(segment.Messages, idMessage("seg"+string(rune('0'+number))+"-"+string(rune('a'+i)), role, text))
	}
	segment.Recap = "## What happened\n- " + texts[0]
	return segment
}

func segmentStores(t *testing.T) map[string]session.Store {
	t.Helper()
	files, err := session.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return map[string]session.Store{"memory": session.NewMemoryStore(), "file": files}
}
