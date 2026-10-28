package gotato

import (
	"context"
	"testing"
	"time"
)

// A message accepted as the Run ends is taken by the host, in the order it was
// accepted, and runs in the next Run of another Session; one that does not fit
// is returned, and DropQueued reports it.
func TestTakeQueuedKeepsAcceptanceOrderAcrossQueues(t *testing.T) {
	first, err := NewSession(context.Background(), WithModel(&recordingModel{scripts: [][]ModelEvent{finalScript("a")}}), WithQueueHandoff())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close(context.Background()) }()
	// Accepted after the Run's last point of taking messages.
	first.endingHook = func() {
		for _, send := range []func(context.Context, Message) error{first.FollowUp, first.Steer, first.FollowUp} {
			if err := send(context.Background(), UserMessage("m")); err != nil {
				t.Error(err)
			}
		}
	}
	seen := recordAll(t, first)
	if _, err := first.Prompt(context.Background(), UserMessage("go")); err != nil {
		t.Fatal(err)
	}
	for _, event := range seen.events() {
		if dropped, ok := event.(MessageDropped); ok {
			t.Fatalf("a message was dropped at the end of a completed Run: %+v", dropped)
		}
	}

	left := first.TakeQueued()
	if len(left) != 3 || left[0].Source != MessageSourceFollowUp || left[1].Source != MessageSourceSteer || left[2].Source != MessageSourceFollowUp {
		t.Fatalf("TakeQueued = %+v, want the three in the order they were accepted", left)
	}
	if again := first.TakeQueued(); len(again) != 0 {
		t.Fatalf("TakeQueued twice = %d messages", len(again))
	}

	limits := DefaultLimits()
	limits.MaxSteerMessages, limits.MaxFollowUpMessages = 1, 1
	second, err := NewSession(context.Background(), WithModel(&recordingModel{}), WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close(context.Background()) }()
	rest := second.Requeue(left)
	if len(rest) != 1 || rest[0].Message.ID != left[2].Message.ID {
		t.Fatalf("Requeue returned %+v, want only the last message", rest)
	}
	second.DropQueued(rest, "queue_full")
}

// A canceled Run still drops what is queued, as before.
func TestACanceledRunStillDropsItsQueue(t *testing.T) {
	model := &recordingModel{scripts: [][]ModelEvent{{{Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}}}, started: make(chan int, 4), gate: make(chan struct{}, 4)}
	session, err := NewSession(context.Background(), WithModel(model), WithQueueHandoff())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	done := make(chan error, 1)
	go func() {
		_, runErr := session.Prompt(context.Background(), UserMessage("cancel me"))
		done <- runErr
	}()
	waitForCall(t, model.started, 0)
	if err := session.FollowUp(context.Background(), UserMessage("leftover")); err != nil {
		t.Fatal(err)
	}
	session.Abort()
	model.gate <- struct{}{}
	<-done
	if left := session.TakeQueued(); len(left) != 0 {
		t.Fatalf("a canceled Run left %d messages for the host", len(left))
	}
}

// A message queued while a Run fails is kept for the host, which decides
// whether another Run takes it.
func TestAFailedRunKeepsItsQueueForTheHost(t *testing.T) {
	model := &recordingModel{scripts: [][]ModelEvent{{{Kind: "unknown"}}}, started: make(chan int, 4), gate: make(chan struct{}, 4)}
	session, err := NewSession(context.Background(), WithModel(model), WithQueueHandoff())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	seen := recordAll(t, session)
	done := make(chan error, 1)
	go func() {
		_, runErr := session.Prompt(context.Background(), UserMessage("fail"))
		done <- runErr
	}()
	// The model sends an event the Loop cannot read, so the Run fails.
	waitForCall(t, model.started, 0)
	if err := session.FollowUp(context.Background(), UserMessage("kept")); err != nil {
		t.Fatal(err)
	}
	model.gate <- struct{}{}
	if err := <-done; err == nil {
		t.Fatal("the Run did not fail")
	}
	left := session.TakeQueued()
	if len(left) != 1 || TextOf(left[0].Message) != "kept" {
		t.Fatalf("TakeQueued = %+v, want the queued message", left)
	}
	for _, event := range seen.events() {
		if _, ok := event.(MessageDropped); ok {
			t.Fatal("the message was dropped though the host could take it")
		}
	}
}

func TestContinueAppendsNoUserMessage(t *testing.T) {
	model := &recordingModel{scripts: [][]ModelEvent{finalScript("first"), finalScript("second"), finalScript("third")}}
	agent, err := NewSession(context.Background(), WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()

	if _, err := agent.Prompt(context.Background(), UserMessage("hello")); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Continue(context.Background()); !IsCode(err, CodeInvalidState) {
		t.Fatalf("Continue after an assistant Message = %v", err)
	}

	if err := agent.FollowUp(context.Background(), UserMessage("carry on")); err != nil {
		t.Fatal(err)
	}
	// The follow-up settles inside its own Run, leaving the transcript ending
	// in an assistant Message again, so a second Continue must still refuse.
	if _, err := agent.Prompt(context.Background(), UserMessage("more")); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Continue(context.Background()); !IsCode(err, CodeInvalidState) {
		t.Fatalf("Continue after a follow-up Run = %v", err)
	}
}

func TestSteerIsConsumedAtTheNextTurnBoundary(t *testing.T) {
	tool := &scriptedTool{}
	model := &recordingModel{
		scripts: [][]ModelEvent{toolCallScript(), finalScript("done")},
		started: make(chan int, 8),
		gate:    make(chan struct{}, 8),
	}
	agent, err := NewSession(context.Background(), WithModel(model), WithTools(tool))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	controllable := agent

	done := make(chan RunResult, 1)
	go func() {
		result, runErr := agent.Prompt(context.Background(), UserMessage("use tool"))
		if runErr != nil {
			t.Error(runErr)
		}
		done <- result
	}()

	waitForCall(t, model.started, 0)
	if err := controllable.Steer(context.Background(), UserMessage("prefer metric units")); err != nil {
		t.Fatal(err)
	}
	// The Steer must not reach the Model call already in flight.
	if containsUserText(model.transcript(0), "prefer metric units") {
		t.Fatal("Steer reached the Model call in flight")
	}
	model.gate <- struct{}{}

	waitForCall(t, model.started, 1)
	if !containsUserText(model.transcript(1), "prefer metric units") {
		t.Fatalf("Steer missing from the next Turn: %+v", model.transcript(1))
	}
	model.gate <- struct{}{}

	select {
	case result := <-done:
		if result.Status != RunStatusCompleted {
			t.Fatalf("result = %+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("steered Run did not settle")
	}
	if model.calls() != 2 {
		t.Fatalf("Model calls = %d", model.calls())
	}
}

func TestSteerAtSettlementKeepsTheRunGoing(t *testing.T) {
	model := &recordingModel{
		scripts: [][]ModelEvent{finalScript("first"), finalScript("second")},
		started: make(chan int, 8),
		gate:    make(chan struct{}, 8),
	}
	agent, err := NewSession(context.Background(), WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	controllable := agent
	seen := recordAll(t, agent)

	done := make(chan RunResult, 1)
	go func() {
		result, runErr := agent.Prompt(context.Background(), UserMessage("hello"))
		if runErr != nil {
			t.Error(runErr)
		}
		done <- result
	}()

	waitForCall(t, model.started, 0)
	if err := controllable.Steer(context.Background(), UserMessage("keep going")); err != nil {
		t.Fatal(err)
	}
	model.gate <- struct{}{}
	waitForCall(t, model.started, 1)
	model.gate <- struct{}{}

	var result RunResult
	select {
	case result = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("steered Run did not settle")
	}
	if TextOf(*result.FinalMessage) != "second" {
		t.Fatalf("final message = %q", TextOf(*result.FinalMessage))
	}

	terminals, steered := 0, 0
	for _, event := range seen.events() {
		switch e := event.(type) {
		case MessageCommitted:
			if e.Source == MessageSourceSteer {
				steered++
			}
		case RunEnded:
			terminals++
		}
	}
	if terminals != 1 || steered != 1 {
		t.Fatalf("terminal events = %d, steer commits = %d", terminals, steered)
	}
}

func TestFollowUpWaitsForSettlement(t *testing.T) {
	tool := &scriptedTool{}
	model := &recordingModel{
		scripts: [][]ModelEvent{toolCallScript(), finalScript("done"), finalScript("after follow-up")},
		started: make(chan int, 8),
		gate:    make(chan struct{}, 8),
	}
	agent, err := NewSession(context.Background(), WithModel(model), WithTools(tool))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	controllable := agent

	done := make(chan RunResult, 1)
	go func() {
		result, runErr := agent.Prompt(context.Background(), UserMessage("use tool"))
		if runErr != nil {
			t.Error(runErr)
		}
		done <- result
	}()

	waitForCall(t, model.started, 0)
	if err := controllable.FollowUp(context.Background(), UserMessage("and then summarize")); err != nil {
		t.Fatal(err)
	}
	model.gate <- struct{}{}

	waitForCall(t, model.started, 1)
	if containsUserText(model.transcript(1), "and then summarize") {
		t.Fatal("FollowUp was consumed at a Turn boundary instead of settlement")
	}
	model.gate <- struct{}{}

	waitForCall(t, model.started, 2)
	if !containsUserText(model.transcript(2), "and then summarize") {
		t.Fatalf("FollowUp missing after settlement: %+v", model.transcript(2))
	}
	model.gate <- struct{}{}

	select {
	case result := <-done:
		if TextOf(*result.FinalMessage) != "after follow-up" {
			t.Fatalf("final message = %q", TextOf(*result.FinalMessage))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run with a follow-up did not settle")
	}
}

func TestControlBuffersAreBounded(t *testing.T) {
	limits := defaultLimits()
	limits.MaxFollowUpMessages = 1
	limits.MaxSteerMessages = 1
	model := &recordingModel{scripts: [][]ModelEvent{finalScript("done")}}
	agent, err := NewSession(context.Background(), WithModel(model), WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	controllable := agent

	if err := controllable.FollowUp(context.Background(), UserMessage("one")); err != nil {
		t.Fatal(err)
	}
	if err := controllable.FollowUp(context.Background(), UserMessage("two")); !IsCode(err, CodeLimitExceeded) {
		t.Fatalf("full follow-up buffer = %v", err)
	}
	if err := controllable.Steer(context.Background(), UserMessage("one")); err != nil {
		t.Fatal(err)
	}
	if err := controllable.Steer(context.Background(), UserMessage("nope")); !IsCode(err, CodeLimitExceeded) {
		t.Fatalf("full Steer buffer = %v", err)
	}
	if err := controllable.Steer(context.Background(), Message{Role: RoleAssistant}); !IsCode(err, CodeInvalidArgument) {
		t.Fatalf("non-user control message = %v", err)
	}
}

func TestControlCommandsRejectedAfterClose(t *testing.T) {
	model := &recordingModel{scripts: [][]ModelEvent{finalScript("done")}}
	agent, err := NewSession(context.Background(), WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	controllable := agent
	if err := agent.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := controllable.Steer(context.Background(), UserMessage("late")); !IsCode(err, CodeSessionClosed) {
		t.Fatalf("Steer after Close = %v", err)
	}
	if err := controllable.FollowUp(context.Background(), UserMessage("late")); !IsCode(err, CodeSessionClosed) {
		t.Fatalf("FollowUp after Close = %v", err)
	}
	if _, err := controllable.Continue(context.Background()); !IsCode(err, CodeSessionClosed) {
		t.Fatalf("Continue after Close = %v", err)
	}
}

func TestAbortCancelsTheCurrentRun(t *testing.T) {
	started := make(chan struct{}, 1)
	block := make(chan struct{})
	model := &testModel{started: started, block: block, scripts: [][]ModelEvent{{{Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}}}}
	agent, err := NewSession(context.Background(), WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	controllable := agent
	controllable.Abort() // no active Run: a no-op, not a panic

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
	controllable.Abort()
	select {
	case err := <-done:
		if !IsCode(err, CodeCanceled) {
			t.Fatalf("aborted Run error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("aborted Run did not return")
	}
	if got := agent.Status(); got != SessionStatusIdle {
		t.Fatalf("status after Abort = %s", got)
	}
}

func TestControlMessagesDoNotSurviveAFailedRun(t *testing.T) {
	model := &recordingModel{
		scripts: [][]ModelEvent{{{Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}}, finalScript("second")},
		started: make(chan int, 8),
		gate:    make(chan struct{}, 8),
	}
	agent, err := NewSession(context.Background(), WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	controllable := agent

	done := make(chan error, 1)
	go func() {
		_, runErr := agent.Prompt(context.Background(), UserMessage("cancel me"))
		done <- runErr
	}()
	waitForCall(t, model.started, 0)
	if err := controllable.FollowUp(context.Background(), UserMessage("leftover")); err != nil {
		t.Fatal(err)
	}
	controllable.Abort()
	model.gate <- struct{}{}
	select {
	case err := <-done:
		if !IsCode(err, CodeCanceled) {
			t.Fatalf("aborted Run error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("aborted Run did not return")
	}

	model.gate <- struct{}{}
	if _, err := agent.Prompt(context.Background(), UserMessage("fresh")); err != nil {
		t.Fatal(err)
	}
	for call := 1; call < model.calls(); call++ {
		if containsUserText(model.transcript(call), "leftover") {
			t.Fatalf("control message survived the failed Run: %+v", model.transcript(call))
		}
	}
}

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
	agent, err := NewSession(context.Background(), WithModel(model), WithTools(tool))
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
	if err := agent.Steer(context.Background(), UserMessage("prefer metric units")); err != nil {
		t.Fatal(err)
	}
	if err := agent.FollowUp(context.Background(), UserMessage("and then summarize")); err != nil {
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
	agent, err := NewSession(context.Background(), WithModel(model))
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
	if err := agent.FollowUp(context.Background(), UserMessage("leftover")); err != nil {
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
	agent, err := NewSession(context.Background(), WithModel(model), WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	seen := recordAll(t, agent)

	if err := agent.FollowUp(context.Background(), UserMessage("one")); err != nil {
		t.Fatal(err)
	}
	if err := agent.FollowUp(context.Background(), UserMessage("two")); !IsCode(err, CodeLimitExceeded) {
		t.Fatalf("full follow-up queue = %v", err)
	}
	if err := agent.Steer(context.Background(), UserMessage("one")); err != nil {
		t.Fatal(err)
	}
	if err := agent.Steer(context.Background(), UserMessage("nope")); !IsCode(err, CodeLimitExceeded) {
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
	agent, err := NewSession(context.Background(), WithModel(model))
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
