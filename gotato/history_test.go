package gotato

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The history part is read alone: no Session, no Run.

func TestHistoryTracksTheEncodedSizeAsItCommits(t *testing.T) {
	h := &sessionHistory{transcript: &memoryTranscript{}}
	for _, text := range []string{"one", "two", "three"} {
		if err := h.commit(CoreLimits{}.withDefaults(), UserMessage(text), true); err != nil {
			t.Fatal(err)
		}
	}
	messages, failure := h.messages()
	if failure != nil || len(messages) != 3 {
		t.Fatalf("messages = %d, %v", len(messages), failure)
	}
	encoded, _ := json.Marshal(messages)
	incremental := h.size
	if want := uint64(len(encoded)) - 2; incremental != want {
		t.Fatalf("size tracked as %d, the encoded array without its brackets is %d", incremental, want)
	}
	// Measuring again gives the same number: it is what a loaded Session starts from.
	h.size = 0
	if err := h.measure(); err != nil || h.size != incremental {
		t.Fatalf("measure = %d, %v, want %d", h.size, err, incremental)
	}
}

func TestHistoryRefusesWhatTheLimitsRefuse(t *testing.T) {
	h := &sessionHistory{transcript: &memoryTranscript{}}
	limits := CoreLimits{MaxMessages: 1}.withDefaults()
	if err := h.commit(limits, UserMessage("first"), true); err != nil {
		t.Fatal(err)
	}
	if err := h.commit(limits, UserMessage("second"), true); err == nil {
		t.Fatal("a Message past MaxMessages was committed")
	}
	// The closing results of a failed Run are exempt.
	if err := h.commit(limits, UserMessage("closing"), false); err != nil {
		t.Fatalf("a commit that does not enforce was refused: %v", err)
	}
	if n, _ := h.len(); n != 2 {
		t.Fatalf("history holds %d Messages, want 2", n)
	}
}

// A Tool Result too large for one Message is replaced by a failed one that says
// why, and that is what the history keeps: the Tool ran, and the Run goes on.
func TestHistoryReplacesAnOversizedToolResult(t *testing.T) {
	h := &sessionHistory{transcript: &memoryTranscript{}}
	limits := CoreLimits{MaxMessageBytes: 1500}.withDefaults()
	big := ToolResult{CallID: "call-1", Status: ToolResultStatusOK, Executed: true, Content: []ContentPart{{Kind: ContentKindText, Text: strings.Repeat("x", 6000)}}}
	message := Message{ID: "m1", Role: RoleToolResult, Parts: cloneParts(big.Content), ToolResult: cloneToolResultPtr(big)}

	committed, result, err := h.commitToolResult(limits, message, big)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != ToolResultStatusFailed || !result.Executed || !strings.Contains(result.SafeError, "too large") {
		t.Fatalf("result = %+v, want the failed stand-in that keeps Executed", result)
	}
	if len(committed.Parts) != 1 || !strings.Contains(committed.Parts[0].Text, "too large") || committed.ToolResult.Status != ToolResultStatusFailed {
		t.Fatalf("committed Message = %+v", committed)
	}
	stored, _ := h.messages()
	if len(stored) != 1 || strings.Contains(stored[0].Parts[0].Text, "xxxx") {
		t.Fatal("the history kept the oversized body")
	}

	// A result that fits is committed as it is.
	small := ToolResult{CallID: "call-2", Status: ToolResultStatusOK, Executed: true, Content: []ContentPart{{Kind: ContentKindText, Text: "ok"}}}
	if _, got, err := h.commitToolResult(limits, Message{ID: "m2", Role: RoleToolResult, Parts: cloneParts(small.Content), ToolResult: cloneToolResultPtr(small)}, small); err != nil || got.Status != ToolResultStatusOK {
		t.Fatalf("a result that fits = %+v, %v", got, err)
	}
}

// The life part wakes WaitForIdle at every change of status, with no lock.
func TestLifeSetStatusWakesWhoWaits(t *testing.T) {
	var l sessionLife
	l.init()
	changed := l.changeSignal()
	select {
	case <-changed:
		t.Fatal("the signal was closed before any change")
	default:
	}
	l.setStatus(SessionStatusBusy)
	select {
	case <-changed:
	case <-time.After(time.Second):
		t.Fatal("a change of status did not close the signal captured before it")
	}
	if decodeSessionStatus(l.status.Load()) != SessionStatusBusy {
		t.Fatal("the status was not stored")
	}
	next := l.changeSignal()
	if next == changed {
		t.Fatal("the signal was not replaced")
	}
	select {
	case <-next:
		t.Fatal("the new signal is already closed")
	default:
	}
}

func TestRunPartCountsAbortWritesToTheirEnd(t *testing.T) {
	var r sessionRun
	r.aborts.Add(1)
	waited := make(chan struct{})
	go func() { r.aborts.Wait(); close(waited) }()
	select {
	case <-waited:
		t.Fatal("Wait returned with an abort write in flight")
	case <-time.After(20 * time.Millisecond):
	}
	r.aborts.Done()
	select {
	case <-waited:
	case <-time.After(time.Second):
		t.Fatal("Wait did not return after the write finished")
	}
}
