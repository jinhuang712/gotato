package gotato

import (
	"context"
	"strings"
	"testing"
)

// queueingJournal is a spill-keeping Journal like session.Record: a write
// that holds a spilled result also returns an Event the Record had queued
// before it (a metadata change), first.
type queueingJournal struct{ journalTranscript }

func (j *queueingJournal) KeepsSpilledResults() bool { return true }

func (j *queueingJournal) Write(ctx context.Context, events []Event) ([]Event, error) {
	for _, event := range events {
		if event.Kind() == EventKindToolResultSpilled {
			queued := ExtensionFailed{Header: event.Head(), Stage: "queued_before"}
			return j.journalTranscript.Write(ctx, append([]Event{queued}, events...))
		}
	}
	return j.journalTranscript.Write(ctx, events)
}

// The Seq of a spilled result is the number of its own Entry, even when the
// Journal returns Events it had queued before it.
func TestSpilledSeqPointsAtTheSpilledEvent(t *testing.T) {
	journal := &queueingJournal{}
	session, err := NewSession(context.Background(), WithModel(echoToolModel("call-1")), WithTools(bigTool{size: SpillPageBytes * 2}), WithTranscript(journal))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	if _, err := session.Prompt(context.Background(), UserMessage("go")); err != nil {
		t.Fatal(err)
	}
	var seq uint64
	for _, m := range session.Transcript().Messages() {
		if m.ToolResult != nil && m.ToolResult.Spilled != nil {
			seq = m.ToolResult.Spilled.Seq
		}
	}
	if seq == 0 {
		t.Fatal("the result was not spilled")
	}
	for _, write := range journal.writes {
		for _, event := range write {
			if event.Head().Number == seq && event.Kind() != EventKindToolResultSpilled {
				t.Fatalf("Spilled.Seq %d is a %s Event, not the spilled body (%s)", seq, event.Kind(), strings.Join(journal.shape(), ", "))
			}
		}
	}
}
