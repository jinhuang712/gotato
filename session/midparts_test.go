package session_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

type blockModel struct{ started chan struct{} }

func (m blockModel) Stream(ctx context.Context, _ gotato.ModelRequest) (gotato.ModelStream, error) {
	close(m.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

// A reply that a limit refuses leaves parts of its text and no Message. When a
// later Run crashes, the load that reads the log for that Run rebuilds only the
// parts of the Run that never ended: the refused reply is not brought back into
// the middle of the history, and every kind of load agrees.
func TestRefusedReplyPartsDoNotReappearAfterACrash(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	record := session.New()
	if _, err := session.Create(ctx, store, record, "a", session.WithIdle()); err != nil {
		t.Fatal(err)
	}
	writer, err := session.OpenWriter(ctx, store, record.ID(), "a")
	if err != nil {
		t.Fatal(err)
	}
	limits := gotato.DefaultLimits()
	limits.MaxMessageBytes = 2000
	big := testkit.NewFakeModel(testkit.Text(strings.Repeat("y", 5000)))
	first, err := gotato.NewSession(gotato.WithModel(big), gotato.WithTranscript(writer), gotato.WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	if result, _ := first.Prompt(ctx, gotato.UserMessage("p1")); result.Status != gotato.RunFailed {
		t.Fatalf("the first Run is %s, want failed on the Message limit", result.Status)
	}
	_ = first.Close(ctx)
	if err := writer.Release(ctx); err != nil {
		t.Fatal(err)
	}

	second, err := session.OpenWriter(ctx, store, record.ID(), "a")
	if err != nil {
		t.Fatal(err)
	}
	turnsBefore := countStored(t, store, record.ID(), gotato.KindTurnStarted)
	started := make(chan struct{})
	live, err := gotato.NewSession(gotato.WithModel(blockModel{started}), gotato.WithTranscript(second))
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = live.Prompt(runCtx, gotato.UserMessage("p2"))
	}()
	<-started
	waitStored(t, store, record.ID(), gotato.KindTurnStarted, turnsBefore+1)

	taken, err := session.OpenWriter(ctx, store, record.ID(), "b") // the crash: B takes over
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	<-done
	_ = live.Close(ctx)
	if err := taken.Release(ctx); err != nil {
		t.Fatal(err)
	}
	for name, messages := range map[string][]gotato.Message{"OpenWriter": taken.Record().Messages()} {
		if got := cutOffTexts(messages); len(got) != 0 {
			t.Fatalf("%s brought back %d refused replies: %v", name, len(got), messages)
		}
	}
	open, err := session.Open(ctx, store, record.ID())
	if err != nil {
		t.Fatal(err)
	}
	view, err := session.View(ctx, store, record.ID())
	if err != nil {
		t.Fatal(err)
	}
	if a, b, c := len(taken.Record().Messages()), len(open.Messages()), len(view.Messages()); a != b || b != c || a != 2 {
		t.Fatalf("OpenWriter has %d Messages, Open %d, View %d; want 2 each", a, b, c)
	}
}
