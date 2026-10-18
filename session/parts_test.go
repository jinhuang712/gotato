package session_test

import (
	"context"
	"strings"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/session"
)

// stallModel streams three 3000-byte deltas and then waits, as a model does
// when the process that called it stops.
type stallModel struct{ sent chan struct{} }

type stallStream struct {
	n    int
	sent chan struct{}
}

func (m stallModel) Stream(context.Context, gotato.ModelRequest) (gotato.ModelStream, error) {
	return &stallStream{sent: m.sent}, nil
}

func (s *stallStream) Recv(ctx context.Context) (gotato.ModelEvent, error) {
	s.n++
	if s.n <= 3 {
		return gotato.ModelEvent{Kind: gotato.ModelTextDelta, Text: strings.Repeat("x", 3000)}, nil
	}
	if s.n == 4 {
		close(s.sent)
	}
	<-ctx.Done()
	return gotato.ModelEvent{}, ctx.Err()
}

func (s *stallStream) Close() error { return nil }

func cutOffTexts(messages []gotato.Message) []string {
	var out []string
	for _, message := range messages {
		if message.StopReason == gotato.StopCutOff {
			out = append(out, gotato.TextOf(message))
		}
	}
	return out
}

// A Writer that stops in the middle of a streaming reply loses at most the last
// part: the Instance that takes the Session over rebuilds the Cut-off reply from
// the parts kept, writes it as a fact, and every later load has it too.
func TestTakeoverRebuildsTheCutOffReplyFromItsParts(t *testing.T) {
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
	sent := make(chan struct{})
	live, err := gotato.NewSession(gotato.WithModel(stallModel{sent: sent}), gotato.WithTranscript(writer))
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = live.Prompt(runCtx, gotato.UserMessage("hi"))
	}()
	<-sent
	time.Sleep(50 * time.Millisecond)

	// The Instance that held the Session stopped: another one takes it over.
	opened, err := session.OpenWriter(ctx, store, record.ID(), "b")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	<-done
	_ = live.Close(ctx)
	want := strings.Repeat("x", 6000) // the two deltas of the last part; the third was never written
	if got := cutOffTexts(opened.Record().Messages()); len(got) != 1 || got[0] != want {
		t.Fatalf("OpenWriter's Messages hold %d Cut-off replies %v, want one of %d bytes", len(got), len(got), len(want))
	}
	if err := opened.Release(ctx); err != nil {
		t.Fatal(err)
	}
	for name, load := range map[string]func() (*session.Record, error){
		"Open": func() (*session.Record, error) { return session.Open(ctx, store, record.ID()) },
		"View": func() (*session.Record, error) { return session.View(ctx, store, record.ID()) },
	} {
		loaded, err := load()
		if err != nil {
			t.Fatal(err)
		}
		if got := cutOffTexts(loaded.Messages()); len(got) != 1 || len(got[0]) != len(want) {
			t.Fatalf("%s holds %d Cut-off replies, want one of %d bytes", name, len(got), len(want))
		}
	}
	events, err := session.StoredEvents(ctx, store, record.ID(), 0)
	if err != nil {
		t.Fatal(err)
	}
	cut := 0
	for _, event := range events {
		if _, ok := event.(gotato.ReplyCutOff); ok {
			cut++
		}
	}
	if cut != 1 {
		t.Fatalf("the Record holds %d reply_cut_off Events, want 1", cut)
	}
}

// A Record that is not stored keeps no parts of a reply: they exist so that a
// crash loses little, and nothing is stored.
func TestUnstoredRecordKeepsNoReplyParts(t *testing.T) {
	ctx := context.Background()
	record := session.New()
	sent := make(chan struct{})
	live, err := gotato.NewSession(gotato.WithModel(stallModel{sent: sent}), gotato.WithTranscript(record))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = live.Close(ctx) }()
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = live.Prompt(runCtx, gotato.UserMessage("hi"))
	}()
	<-sent
	cancel()
	<-done
	for _, event := range record.Events() {
		if event.Kind() == gotato.KindReplyPartKept {
			t.Fatalf("an unstored Record holds %s", event.Kind())
		}
	}
}
