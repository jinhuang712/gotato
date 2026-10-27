package gotato

import (
	"context"
	"testing"
	"time"
)

// indexOfKind returns the position of the first Event of kind at or after
// from, or -1.
func indexOfKind(events []Event, kind EventKind, from int) int {
	for i := from; i < len(events); i++ {
		if events[i].Kind() == kind {
			return i
		}
	}
	return -1
}

// A Steer and a FollowUp reach the Record as MessageQueued and then
// MessageCommitted, with the source, the same Message ID, and the numbers of
// the Record in order.
func TestSteerAndFollowUpAreQueuedThenCommitted(t *testing.T) {
	tool := &scriptedTool{}
	model := &recordingModel{
		scripts: [][]ModelEvent{toolCallScript(), finalScript("done"), finalScript("after follow-up")},
		started: make(chan int, 8),
		gate:    make(chan struct{}, 8),
	}
	agent, err := NewSession(WithModel(model), WithTools(tool))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	seen := recordAll(t, agent)

	done := make(chan error, 1)
	go func() {
		_, runErr := agent.Prompt(context.Background(), UserMessage("use tool"))
		done <- runErr
	}()
	waitForCall(t, model.started, 0)
	if err := agent.Steer(UserMessage("prefer metric units")); err != nil {
		t.Fatal(err)
	}
	if err := agent.FollowUp(UserMessage("and then summarize")); err != nil {
		t.Fatal(err)
	}
	model.gate <- struct{}{}
	waitForCall(t, model.started, 1)
	model.gate <- struct{}{}
	waitForCall(t, model.started, 2)
	model.gate <- struct{}{}
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	events := seen.events()
	for _, want := range []struct {
		source MessageSource
		text   string
	}{{MessageSourceSteer, "prefer metric units"}, {MessageSourceFollowUp, "and then summarize"}} {
		var queued *MessageQueued
		var committed *MessageCommitted
		queuedAt, committedAt := -1, -1
		for i, event := range events {
			switch e := event.(type) {
			case MessageQueued:
				if e.Source == want.source {
					queued, queuedAt = &e, i
				}
			case MessageCommitted:
				if e.Source == want.source {
					committed, committedAt = &e, i
				}
			}
		}
		if queued == nil || committed == nil {
			t.Fatalf("%s: queued %v, committed %v in %v", want.source, queued, committed, seen.kinds())
		}
		if queuedAt > committedAt || queued.Number >= committed.Number {
			t.Fatalf("%s: MessageQueued (#%d at %d) must come before MessageCommitted (#%d at %d)",
				want.source, queued.Number, queuedAt, committed.Number, committedAt)
		}
		if queued.MessageID == "" || queued.MessageID != committed.Message.ID || TextOf(committed.Message) != want.text {
			t.Fatalf("%s: queued %+v, committed %+v", want.source, queued, committed)
		}
	}
}

// A control message still queued when the Run ends is dropped, and the Record
// says so before the RunEnded.
func TestLeftoverControlMessageIsDropped(t *testing.T) {
	model := &recordingModel{
		scripts: [][]ModelEvent{{{Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}}},
		started: make(chan int, 8),
		gate:    make(chan struct{}, 8),
	}
	agent, err := NewSession(WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	seen := recordAll(t, agent)

	done := make(chan error, 1)
	go func() {
		_, runErr := agent.Prompt(context.Background(), UserMessage("cancel me"))
		done <- runErr
	}()
	waitForCall(t, model.started, 0)
	if err := agent.FollowUp(UserMessage("leftover")); err != nil {
		t.Fatal(err)
	}
	agent.Abort()
	model.gate <- struct{}{}
	if err := <-done; !IsCode(err, CodeCanceled) {
		t.Fatalf("aborted Run error = %v", err)
	}

	events := seen.events()
	var queued *MessageQueued
	var dropped *MessageDropped
	for _, event := range events {
		switch e := event.(type) {
		case MessageQueued:
			queued = &e
		case MessageDropped:
			dropped = &e
		case MessageCommitted:
			if e.Source != MessageSourcePrompt {
				t.Fatalf("a dropped message was committed: %+v", e)
			}
		}
	}
	if queued == nil || dropped == nil {
		t.Fatalf("queued %v, dropped %v in %v", queued, dropped, seen.kinds())
	}
	if dropped.MessageID != queued.MessageID || dropped.Source != MessageSourceFollowUp || dropped.Why != "run_ended" {
		t.Fatalf("MessageDropped = %+v for %+v", dropped, queued)
	}
	if droppedAt, endAt := indexOfKind(events, EventKindMessageDropped, 0), indexOfKind(events, EventKindRunEnded, 0); droppedAt < 0 || droppedAt > endAt {
		t.Fatalf("MessageDropped must come before RunEnded: %v", seen.kinds())
	}
}

// A full queue produces a MessageRejected and a limit_exceeded error.
func TestFullQueueRejectsWithAnEvent(t *testing.T) {
	limits := defaultLimits()
	limits.MaxFollowUpMessages = 1
	limits.MaxSteerMessages = 1
	model := &recordingModel{scripts: [][]ModelEvent{finalScript("done")}}
	agent, err := NewSession(WithModel(model), WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	seen := recordAll(t, agent)

	if err := agent.FollowUp(UserMessage("one")); err != nil {
		t.Fatal(err)
	}
	if err := agent.FollowUp(UserMessage("two")); !IsCode(err, CodeLimitExceeded) {
		t.Fatalf("full follow-up queue = %v", err)
	}
	if err := agent.Steer(UserMessage("one")); err != nil {
		t.Fatal(err)
	}
	if err := agent.Steer(UserMessage("nope")); !IsCode(err, CodeLimitExceeded) {
		t.Fatalf("full Steer queue = %v", err)
	}
	var rejected []MessageRejected
	queued := 0
	for _, event := range seen.events() {
		switch e := event.(type) {
		case MessageRejected:
			rejected = append(rejected, e)
		case MessageQueued:
			queued++
		}
	}
	if queued != 2 || len(rejected) != 2 {
		t.Fatalf("queued %d, rejected %+v in %v", queued, rejected, seen.kinds())
	}
	if rejected[0].Source != MessageSourceFollowUp || rejected[0].Why != "queue_full" ||
		rejected[1].Source != MessageSourceSteer || rejected[1].Why != "queue_full" {
		t.Fatalf("rejections = %+v", rejected)
	}
	for _, event := range rejected {
		if event.MessageID == "" || event.Number == 0 {
			t.Fatalf("MessageRejected is not a stored, identified Event: %+v", event)
		}
	}
}

// An Abort is recorded as AbortRequested before the Run's RunEnded.
func TestAbortIsRecordedBeforeRunEnded(t *testing.T) {
	started := make(chan struct{}, 1)
	block := make(chan struct{})
	model := &testModel{started: started, block: block, scripts: [][]ModelEvent{{{Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}}}}
	agent, err := NewSession(WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	seen := recordAll(t, agent)

	done := make(chan error, 1)
	go func() {
		_, runErr := agent.Prompt(context.Background(), UserMessage("abort me"))
		done <- runErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("Model was not called")
	}
	agent.Abort()
	if err := <-done; !IsCode(err, CodeCanceled) {
		t.Fatalf("aborted Run error = %v", err)
	}
	events := seen.events()
	abortAt, endAt := indexOfKind(events, EventKindAbortRequested, 0), indexOfKind(events, EventKindRunEnded, 0)
	if abortAt < 0 || endAt < 0 || abortAt > endAt {
		t.Fatalf("AbortRequested at %d, RunEnded at %d in %v", abortAt, endAt, seen.kinds())
	}
	if events[abortAt].Head().RunID != events[endAt].Head().RunID || events[abortAt].Head().RunID == "" {
		t.Fatalf("AbortRequested names Run %q, RunEnded Run %q", events[abortAt].Head().RunID, events[endAt].Head().RunID)
	}
	if ended := events[endAt].(RunEnded); ended.Status != RunStatusCanceled {
		t.Fatalf("RunEnded = %+v", ended)
	}
}

// A reasoning delta from the model is sent as a ReasoningDelta, and never
// stored.
func TestReasoningDeltaIsSentNotStored(t *testing.T) {
	model := &recordingModel{scripts: [][]ModelEvent{{
		{Kind: ModelEventKindReasoningDelta, Text: "think"},
		{Kind: ModelEventKindReasoningDelta, Text: " hard"},
		{Kind: ModelEventKindTextDelta, Text: "answer"},
		{Kind: ModelEventKindDone, StopReason: StopReasonEndTurn},
	}}}
	agent, err := NewSession(WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	seen := recordAll(t, agent)
	if _, err := agent.Prompt(context.Background(), UserMessage("hi")); err != nil {
		t.Fatal(err)
	}
	var text []string
	for _, event := range seen.events() {
		if e, ok := event.(ReasoningDelta); ok {
			text = append(text, e.Text)
			if e.Kind().Class() != EventClassDelta || e.MessageID == "" {
				t.Fatalf("ReasoningDelta = %+v", e)
			}
		}
	}
	if len(text) != 2 || text[0] != "think" || text[1] != " hard" {
		t.Fatalf("ReasoningDelta texts = %q", text)
	}
}
