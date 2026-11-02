package session_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/record"

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

func storedKinds(t *testing.T, store session.Records, id string, after uint64) []gotato.EventKind {
	t.Helper()
	events, err := session.StoredEvents(context.Background(), store, id, after, 0)
	if err != nil {
		t.Fatal(err)
	}
	return kindsOf(events)
}

func runSession(t *testing.T, s *record.Record, model gotato.Model, prompt string) gotato.RunResult {
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

// fakeClock is a clock a test moves by hand, for a store that times rows.
type fakeClock struct {
	mu sync.Mutex
	at time.Duration
}

var fakeStart = time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return fakeStart.Add(c.at)
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at += d
}

// headerState is the part of a Header's State these tests read: the Run in
// flight, and whether the writer holds the Session.
type headerState struct {
	Running gotato.RunID `json:"running"`
	Busy    bool         `json:"busy"`
}

// stateOf decodes the State of the Header a Load returned.
func stateOf(t *testing.T, loaded record.Loaded) headerState {
	t.Helper()
	var state headerState
	if err := json.Unmarshal(loaded.Header.State, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func fileEntry(seq uint64, kind gotato.EventKind) record.Entry {
	return record.Entry{Seq: seq, Kind: kind, Layer: record.LayerFact, Data: []byte(`{}`)}
}

// fileSummary is the Header of a write that leaves Record "r" at seq: its
// Info.Messages names the position.
func fileSummary(seq uint64) record.Header {
	at := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	return record.Header{Epoch: "e1", Info: record.Info{ID: "r", CreatedAt: at, UpdatedAt: at, Messages: int(seq)}}
}

// countingRecords counts the Loads and Logs that reach the Records it wraps.
type countingRecords struct {
	session.Records
	mu          sync.Mutex
	loads, logs int
}

func (c *countingRecords) Load(ctx context.Context, id string) (record.Loaded, error) {
	c.mu.Lock()
	c.loads++
	c.mu.Unlock()
	return c.Records.Load(ctx, id)
}

func (c *countingRecords) Log(ctx context.Context, id string, after uint64, limit int) ([]record.Entry, error) {
	c.mu.Lock()
	c.logs++
	c.mu.Unlock()
	return c.Records.Log(ctx, id, after, limit)
}

func (c *countingRecords) counts() (loads, logs int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loads, c.logs
}
