package gotato

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The Events of a step ride its write: the steps of a Run with two tool calls,
// in order, with the Events each one holds.
func TestJournalWritesEachStepWithItsEvents(t *testing.T) {
	journal := &journalTranscript{}
	session, err := NewSession(context.Background(), WithModel(echoToolModel("call-1", "call-2")), WithTools(&scriptedTool{}), WithTranscript(journal))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	if _, err := session.Prompt(context.Background(), UserMessage("go")); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"run_started+message_committed",
		"turn_started+context_built+reply_started", "reply_ended",
		"tool_started", "tool_ended+tool_result_committed",
		"tool_started", "tool_ended+tool_result_committed",
		"turn_ended+turn_started+context_built+reply_started", "reply_ended",
		"turn_ended+run_ended",
	}
	if got := journal.shape(); !reflect.DeepEqual(got, want) {
		t.Fatalf("writes =\n%v\nwant\n%v", got, want)
	}
	// A step comes after the Messages it makes durable: the prompt is the
	// first Message, each tool result one more, and so on.
	if want := []int{1, 1, 2, 2, 3, 3, 4, 4, 5, 5}; !reflect.DeepEqual(journal.before, want) {
		t.Fatalf("Messages committed before each write = %v, want %v", journal.before, want)
	}
	// Numbers count the Events written, from 1, with no gap.
	var number uint64
	for _, write := range journal.writes {
		for _, event := range write {
			number++
			if event.Head().Number != number {
				t.Fatalf("%s has number %d, want %d", event.Kind(), event.Head().Number, number)
			}
			if event.Head().SessionID != session.ID() {
				t.Fatalf("%s carries session %q", event.Kind(), event.Head().SessionID)
			}
		}
	}
}

func TestToolResultsCommitPerGroup(t *testing.T) {
	journal := &journalTranscript{}
	session, err := NewSession(context.Background(), WithModel(echoToolModel("call-1", "call-2")), WithTools(&scriptedTool{}), WithTranscript(journal))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	if _, err := session.Prompt(context.Background(), UserMessage("go")); err != nil {
		t.Fatal(err)
	}
	// One Tool runs at a time by default, so the first call's result is
	// committed before the second call starts.
	messages := journal.Messages()
	if len(messages) != 5 || messages[2].ToolResult == nil || messages[2].ToolResult.CallID != "call-1" || messages[3].ToolResult.CallID != "call-2" {
		t.Fatalf("transcript = %+v", messages)
	}
	if journal.before[5] != 3 {
		t.Fatalf("the second tool_started came after %d Messages, want 3 (its predecessor's result committed)", journal.before[5])
	}
}

func TestReplyPartsMergeDeltas(t *testing.T) {
	delta := strings.Repeat("a", 100)
	var events []ModelEvent
	for range 100 {
		events = append(events, ModelEvent{Kind: ModelEventKindTextDelta, Text: delta})
	}
	events = append(events, ModelEvent{Kind: ModelEventKindDone, StopReason: StopReasonEndTurn})
	journal := &journalTranscript{}
	session, err := NewSession(context.Background(), WithModel(&testModel{scripts: [][]ModelEvent{events}}), WithTranscript(journal))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	if _, err := session.Prompt(context.Background(), UserMessage("go")); err != nil {
		t.Fatal(err)
	}
	var parts []ReplyPartKept
	for _, write := range journal.writes {
		for _, event := range write {
			if part, ok := event.(ReplyPartKept); ok {
				parts = append(parts, part)
			}
		}
	}
	// 10,000 bytes in 100 deltas make two parts of at least 4 KiB each, not a
	// write per delta.
	if len(parts) != 2 {
		t.Fatalf("reply parts = %d, want 2", len(parts))
	}
	for _, part := range parts {
		if len(part.Text) < replyPartBytes || part.MessageID == "" {
			t.Fatalf("part = %d bytes for %q", len(part.Text), part.MessageID)
		}
	}
}

func TestJournalFailureStopsBeforeTheModelCall(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code ErrorCode
	}{
		{name: "conflict", err: ErrorOf(CodeConflict, "another writer"), code: CodeConflict},
		{name: "other failure", err: errors.New("disk full"), code: CodeStoreFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &testModel{scripts: [][]ModelEvent{{{Kind: ModelEventKindTextDelta, Text: "hi"}, {Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}}}}
			journal := &journalTranscript{failAt: EventKindReplyStarted, failErr: tc.err}
			session, err := NewSession(context.Background(), WithModel(model), WithTranscript(journal))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = session.Close(context.Background()) }()
			result, err := session.Prompt(context.Background(), UserMessage("go"))
			if err == nil || result.Error == nil || result.Error.Code != tc.code {
				t.Fatalf("result = %+v, err = %v; want %s", result, err, tc.code)
			}
			model.mu.Lock()
			calls := model.calls
			model.mu.Unlock()
			if calls != 0 {
				t.Fatalf("the model was called %d times after its step failed", calls)
			}
		})
	}
}

// An Event reaches a subscriber only after its write: while the write waits,
// the subscriber has not seen it, and when the write fails it never does.
func TestEventsAreSentAfterTheirWrite(t *testing.T) {
	hold := make(chan struct{})
	writing := make(chan struct{}, 1)
	journal := &journalTranscript{hold: hold, writing: writing, holdKinds: map[EventKind]bool{EventKindMessageCommitted: true}}
	session, err := NewSession(context.Background(), WithModel(&testModel{scripts: [][]ModelEvent{{{Kind: ModelEventKindTextDelta, Text: "hi"}, {Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}}}}), WithTranscript(journal))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	sub, err := session.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = session.Prompt(context.Background(), UserMessage("go"))
	}()
	<-writing
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if event, err := sub.Next(ctx); err == nil {
		t.Fatalf("%s was sent before its write finished", event.Kind())
	}
	close(hold)
	<-done
	if event := next(t, sub); event.Kind() != EventKindRunStarted || event.Head().Number != 1 {
		t.Fatalf("first Event after the write = %s numbered %d", event.Kind(), event.Head().Number)
	}
}

func TestFailedWriteSendsNothing(t *testing.T) {
	journal := &journalTranscript{failAt: EventKindReplyStarted, failErr: errors.New("disk full")}
	session, err := NewSession(context.Background(), WithModel(&testModel{scripts: [][]ModelEvent{{{Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}}}}), WithTranscript(journal))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	rec := recordAll(t, session)
	_, _ = session.Prompt(context.Background(), UserMessage("go"))
	for _, event := range rec.events() {
		switch event.Kind() {
		case EventKindTurnStarted, EventKindContextBuilt, EventKindReplyStarted:
			t.Fatalf("%s was sent although its write failed", event.Kind())
		}
	}
}

// A delta is sent at once and carries the number of the stored Event it
// follows; it is not stored.
func TestDeltasCarryTheNumberOfTheEventTheyFollow(t *testing.T) {
	journal := &journalTranscript{}
	model := &testModel{scripts: [][]ModelEvent{{
		{Kind: ModelEventKindReasoningDelta, Text: "think"}, {Kind: ModelEventKindTextDelta, Text: "a"}, {Kind: ModelEventKindTextDelta, Text: "b"}, {Kind: ModelEventKindDone, StopReason: StopReasonEndTurn},
	}}}
	session, err := NewSession(context.Background(), WithModel(model), WithTranscript(journal))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	rec := recordAll(t, session)
	if _, err := session.Prompt(context.Background(), UserMessage("go")); err != nil {
		t.Fatal(err)
	}
	var started uint64
	deltas := 0
	for _, event := range rec.events() {
		switch e := event.(type) {
		case ReplyStarted:
			started = e.Number
		case TextDelta, ReasoningDelta:
			deltas++
			if e.Head().Number != started {
				t.Fatalf("%s numbered %d, want %d, the number of the reply_started it follows", e.Kind(), e.Head().Number, started)
			}
		}
	}
	if deltas != 3 {
		t.Fatalf("deltas = %d, want 3", deltas)
	}
	for _, kind := range journal.kinds() {
		if kind.Class() == EventClassDelta {
			t.Fatalf("%s was stored", kind)
		}
	}
}

// A Session without a store still numbers and sends its Events.
func TestSessionWithoutAStoreNumbersItsEvents(t *testing.T) {
	session, err := NewSession(context.Background(), WithModel(&testModel{scripts: [][]ModelEvent{{{Kind: ModelEventKindTextDelta, Text: "hi"}, {Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}}}}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	rec := recordAll(t, session)
	if _, err := session.Prompt(context.Background(), UserMessage("go")); err != nil {
		t.Fatal(err)
	}
	var number uint64
	for _, event := range rec.events() {
		if event.Kind().Class() != EventClassStored {
			continue
		}
		number++
		if event.Head().Number != number {
			t.Fatalf("%s numbered %d, want %d", event.Kind(), event.Head().Number, number)
		}
	}
	if number == 0 {
		t.Fatal("no stored Events were sent")
	}
}

// A Journal's own error text stays on the Cause: Message reaches clients.
func TestJournalErrorKeepsForeignTextOffMessage(t *testing.T) {
	failure := journalError(errors.New("dsn=postgres://user:hunter2@db"))
	if failure.Code != CodeStoreFailure || strings.Contains(failure.Message, "hunter2") {
		t.Fatalf("journalError = %+v", failure)
	}
	if failure.Cause == nil || !strings.Contains(failure.Cause.Error(), "hunter2") {
		t.Fatalf("Cause = %v, want the original error", failure.Cause)
	}
}

// ctxJournal is a Journal that honors its ctx: a write under an ended ctx fails.
type ctxJournal struct{ journalTranscript }

func (j *ctxJournal) Write(ctx context.Context, events []Event) ([]Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return j.journalTranscript.Write(ctx, events)
}

// The Run's last write does not die with the caller's ctx: a Journal that
// honors its ctx still receives run_ended after a deadline.
func TestFinalWriteSurvivesCanceledContext(t *testing.T) {
	journal := &ctxJournal{}
	block := make(chan struct{})
	defer close(block)
	model := &testModel{scripts: [][]ModelEvent{{{Kind: ModelEventKindTextDelta, Text: "x"}}}, block: block}
	session, err := NewSession(context.Background(), WithModel(model), WithTranscript(journal))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	result, _ := session.Prompt(ctx, UserMessage("go"))
	if result.Status != RunStatusDeadlineExceeded {
		t.Fatalf("status = %q", result.Status)
	}
	kinds := journal.kinds()
	if len(kinds) == 0 || kinds[len(kinds)-1] != EventKindRunEnded {
		t.Fatalf("stored kinds = %v, want run_ended last", kinds)
	}
}
