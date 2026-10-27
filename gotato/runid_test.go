package gotato

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func runIDsOf(messages []Message) map[RunID]int {
	out := map[RunID]int{}
	for _, message := range messages {
		out[message.RunID]++
	}
	return out
}

// Every Message a Run commits carries the Run's ID: the prompt, each reply,
// and each tool result; a later Run's carry its own.
func TestMessagesCarryTheRunThatCommittedThem(t *testing.T) {
	model := &recordingModel{scripts: [][]ModelEvent{toolCallScript(), finalScript("done"), finalScript("second")}}
	agent, err := NewSession(WithModel(model), WithTools(&scriptedTool{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	first, err := agent.Prompt(context.Background(), UserMessage("use tool"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := agent.Prompt(context.Background(), UserMessage("again"))
	if err != nil {
		t.Fatal(err)
	}
	messages := agent.Transcript().Messages()
	if len(messages) != 6 {
		t.Fatalf("%d Messages, want 6 (prompt, tool call, tool result, reply; prompt, reply)", len(messages))
	}
	for i, message := range messages {
		want := first.RunID
		if i >= 4 {
			want = second.RunID
		}
		if message.RunID != want || want == "" {
			t.Fatalf("Message %d (%s) has RunID %q, want %q", i, message.Role, message.RunID, want)
		}
	}
	if first.RunID == second.RunID {
		t.Fatal("two Runs share a RunID")
	}
}

// A Steer and a FollowUp the Run takes in belong to that Run.
func TestSteeredMessagesCarryTheirRun(t *testing.T) {
	model := &recordingModel{
		scripts: [][]ModelEvent{toolCallScript(), finalScript("done"), finalScript("after follow-up")},
		started: make(chan int, 8),
		gate:    make(chan struct{}, 8),
	}
	agent, err := NewSession(WithModel(model), WithTools(&scriptedTool{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	type outcome struct {
		result RunResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, runErr := agent.Prompt(context.Background(), UserMessage("use tool"))
		done <- outcome{result, runErr}
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
	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	steered := 0
	for _, message := range agent.Transcript().Messages() {
		if message.RunID != got.result.RunID {
			t.Fatalf("%s Message %q has RunID %q, want the Run's %q", message.Role, TextOf(message), message.RunID, got.result.RunID)
		}
		if text := TextOf(message); text == "prefer metric units" || text == "and then summarize" {
			steered++
		}
	}
	if steered != 2 {
		t.Fatalf("found %d steered Messages, want 2", steered)
	}
}

// The continuation the Loop writes after a cut-off Turn belongs to the Run.
func TestContinuationMessageCarriesTheRun(t *testing.T) {
	cut := []ModelEvent{{Kind: ModelEventKindReasoningDelta, Text: "thinking"}, {Kind: ModelEventKindDone, StopReason: StopReasonMaxTokens}}
	limits := DefaultLimits()
	limits.MaxTruncatedContinuations = 2
	model := &recordingModel{scripts: [][]ModelEvent{cut, finalScript("done")}}
	agent, err := NewSession(WithModel(model), WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	result, err := agent.Prompt(context.Background(), UserMessage("work"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range agent.Transcript().Messages() {
		if message.RunID != result.RunID {
			t.Fatalf("%s Message has RunID %q, want %q", message.Role, message.RunID, result.RunID)
		}
		found = found || TextOf(message) == TruncatedContinuationText
	}
	if !found {
		t.Fatal("no continuation Message in the Transcript")
	}
}

// History a Session starts with did not come from a Run: its RunID stays
// empty, and the Run's own Messages carry the Run's.
func TestHistoryOutsideARunHasNoRunID(t *testing.T) {
	history := &memoryTranscript{messages: []Message{UserMessage("earlier"), AssistantMessage("earlier reply")}}
	model := &recordingModel{scripts: [][]ModelEvent{finalScript("ok")}}
	agent, err := NewSession(WithModel(model), WithTranscript(history))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	result, err := agent.Prompt(context.Background(), UserMessage("now"))
	if err != nil {
		t.Fatal(err)
	}
	counts := runIDsOf(agent.Transcript().Messages())
	if counts[""] != 2 || counts[result.RunID] != 2 {
		t.Fatalf("RunIDs = %v, want 2 empty and 2 of the Run", counts)
	}
}

// A RunID is a record. It must not reach the model: ForModel drops it, the
// request built from a transcript is the same bytes with or without it, and
// the model sees nothing of it.
func TestRunIDNeverReachesTheModel(t *testing.T) {
	with := []Message{
		{ID: "m1", RunID: "run-secret-1", Role: RoleUser, Parts: []ContentPart{{Kind: ContentKindText, Text: "hi"}}},
		{ID: "m2", RunID: "run-secret-1", Role: RoleAssistant, Parts: []ContentPart{{Kind: ContentKindText, Text: "hello"}}},
	}
	without := []Message{with[0], with[1]}
	without[0].RunID, without[1].RunID = "", ""

	got, _ := json.Marshal(ForModel(with))
	want, _ := json.Marshal(ForModel(without))
	if string(got) != string(want) || strings.Contains(string(got), "run-secret") {
		t.Fatalf("ForModel kept the RunID:\n%s\nwant\n%s", got, want)
	}

	a := &recordingModel{scripts: [][]ModelEvent{finalScript("ok")}}
	b := &recordingModel{scripts: [][]ModelEvent{finalScript("ok")}}
	for model, history := range map[*recordingModel][]Message{a: with, b: without} {
		agent, err := NewSession(WithModel(model), WithTranscript(&memoryTranscript{messages: append([]Message(nil), history...)}))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := agent.Prompt(context.Background(), UserMessage("next")); err != nil {
			t.Fatal(err)
		}
		_ = agent.Close(context.Background())
	}
	requestA, _ := json.Marshal(a.requests[0])
	requestB, _ := json.Marshal(b.requests[0])
	if string(requestA) != string(requestB) || strings.Contains(string(requestA), "run-secret") {
		t.Fatalf("a RunID changed the request:\n%s\n%s", requestA, requestB)
	}
}
