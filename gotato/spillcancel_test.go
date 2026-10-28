package gotato

import (
	"context"
	"testing"
)

// spillCtxJournal is a spill-keeping Journal that, like a real store, refuses a
// write whose context has ended.
type spillCtxJournal struct{ journalTranscript }

func (j *spillCtxJournal) KeepsSpilledResults() bool { return true }

func (j *spillCtxJournal) Write(ctx context.Context, events []Event) ([]Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return j.journalTranscript.Write(ctx, events)
}

// cancelAfterTool cancels the caller's context once the Tool has run, before
// its result is committed.
type cancelAfterTool struct{ cancel context.CancelFunc }

func (c cancelAfterTool) After(_ context.Context, result ToolResult) (ToolResult, error) {
	c.cancel()
	return result, nil
}

// A Tool that ran keeps its large result when the Run is canceled before the
// result is committed: the spill is written under the final context, as the
// commit of a result is.
func TestCanceledRunStillCommitsALargeToolResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session, err := NewSession(context.Background(), WithModel(echoToolModel("call-1")),
		WithTools(bigTool{size: SpillPageBytes * 2}), WithExtension(cancelAfterTool{cancel}), WithTranscript(&spillCtxJournal{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	_, _ = session.Prompt(ctx, UserMessage("go"))
	for _, m := range session.Transcript().Messages() {
		if m.Role != RoleToolResult {
			continue
		}
		if m.ToolResult.Spilled == nil {
			t.Fatalf("the Tool ran but its result was not kept: %q", m.ToolResult.SafeError)
		}
		return
	}
	t.Fatal("no tool result")
}
