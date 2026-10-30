package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/examples/larkbot/internal/fakelark"
	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// pieces streams "p0 p1 ... p(n-1) " one delta at a time, gap apart.
func pieces(n int, gap time.Duration) *testkit.FakeModel {
	var s testkit.Script
	for i := range n {
		s = append(s, gotato.ModelEvent{Kind: gotato.ModelEventKindTextDelta, Text: "p" + string(rune('0'+i)) + " "})
	}
	s = append(s, gotato.ModelEvent{Kind: gotato.ModelEventKindDone, StopReason: gotato.StopReasonEndTurn})
	m := testkit.NewFakeModel(s)
	m.Delay = gap
	return m
}

const allPieces = "p0 p1 p2 p3 p4 p5 p6 p7"

func streamingBot(t *testing.T, model gotato.Model, every time.Duration) (*fakelark.Server, *larkInstance) {
	t.Helper()
	store, _ := session.NewFileStore(t.TempDir())
	h, _, start := newFake(t)
	a := newLarkInstance(t, store, "pod-a", h.url, model)
	a.bot.streamEvery = every
	start(fakelark.Config{Bots: []string{a.srv.URL}})
	return h.srv, a
}

// One card is posted, updated while the model writes, and ends as the final
// text. Its own id (not the user's message id) is in the Session's metadata,
// and the Lark message_id is the Run's request id.
func TestStreamingCardIsUpdatedInPlace(t *testing.T) {
	fl, a := streamingBot(t, pieces(8, 30*time.Millisecond), 60*time.Millisecond)
	sent := fl.Deliver(context.Background(), "oc_x", "hello", "", "")
	waitFor(t, "the final text on the card", func() bool {
		c := fl.Cards("", "", time.Time{})
		return len(c) == 1 && strings.TrimSpace(c[0].Text) == allPieces
	})
	time.Sleep(200 * time.Millisecond) // a second card or a late update would show by now
	cards := fl.Cards("", "", time.Time{})
	if len(cards) != 1 {
		t.Fatalf("cards = %d, want 1 (the replyLoop must not post a second one)", len(cards))
	}
	if cards[0].ReplyTo != sent.MessageID || cards[0].Updates < 3 {
		t.Fatalf("card %+v: want a reply to the user's message and several updates", cards[0])
	}

	id := sessionIDFor(sent.MessageID)
	rec, err := a.runner.Record(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := rec.Get(metaCard); got != cards[0].MessageID || got == sent.MessageID {
		t.Fatalf("metadata %s = %q, want the card's id %q", metaCard, got, cards[0].MessageID)
	}
	events, err := a.runner.Events(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var started []string
	for _, e := range events {
		if rs, ok := e.(gotato.RunStarted); ok {
			started = append(started, rs.RequestID)
		}
	}
	if len(started) != 1 || started[0] != sent.MessageID {
		t.Fatalf("run_started request ids = %q, want [%s]", started, sent.MessageID)
	}
}

// The interval caps the update rate: with a long one, only the final update is made.
func TestStreamingCardUpdatesAtMostOncePerInterval(t *testing.T) {
	fl, _ := streamingBot(t, pieces(8, 10*time.Millisecond), time.Hour)
	fl.Deliver(context.Background(), "oc_x", "hello", "", "")
	waitFor(t, "the final text on the card", func() bool {
		c := fl.Cards("", "", time.Time{})
		return len(c) == 1 && strings.TrimSpace(c[0].Text) == allPieces
	})
	if c := fl.Cards("", "", time.Time{})[0]; c.Updates != 1 {
		t.Fatalf("updates = %d, want only the final one", c.Updates)
	}
}

// A message sent into a Run that is going is queued: no second placeholder
// card, and the Run's card gets the final text.
func TestQueuedMessageDoesNotMakeACard(t *testing.T) {
	fl, a := streamingBot(t, pieces(8, 40*time.Millisecond), 60*time.Millisecond)
	first := fl.Deliver(context.Background(), "oc_x", "first", "", "")
	waitFor(t, "the first card", func() bool { return len(fl.Cards("", "", time.Time{})) == 1 })
	fl.Deliver(context.Background(), "oc_x", "second", first.MessageID, first.MessageID)
	id := sessionIDFor(first.MessageID)
	waitFor(t, "both messages in the Transcript", func() bool { return len(userTexts(t, a.runner, id)) == 2 })
	waitFor(t, "the final text", func() bool {
		c := fl.Cards("", "", time.Time{})
		return len(c) >= 1 && strings.Contains(c[0].Text, allPieces)
	})
	time.Sleep(300 * time.Millisecond)
	for _, c := range fl.Cards("", "", time.Time{}) {
		t.Logf("card %s reply_to=%s updates=%d text=%q", c.MessageID, c.ReplyTo, c.Updates, c.Text)
		if c.Text == thinking {
			t.Fatalf("card %s was left as a placeholder", c.MessageID)
		}
	}
	if n := len(fl.Cards("", "", time.Time{})); n != 1 && n != 2 {
		t.Fatalf("cards = %d", n)
	}
}

// A message that lands on the instance without the Run is forwarded, and the
// placeholder the refused start posted is recalled: one card is left.
func TestRefusedStartLeavesNoPlaceholder(t *testing.T) {
	store, _ := session.NewFileStore(t.TempDir())
	h, _, start := newFake(t)
	entered, release := make(chan struct{}), make(chan struct{})
	a := newLarkInstance(t, store, "pod-a", h.url, gatedModel(entered, release))
	b := newLarkInstance(t, store, "pod-b", h.url, slowModel(0))
	start(fakelark.Config{Bots: []string{a.srv.URL, b.srv.URL}, Seed: seedPicking(2, 0, 1)})

	first := h.srv.Deliver(context.Background(), "oc_x", "first", "", "")
	<-entered
	reply := h.srv.Deliver(context.Background(), "oc_x", "r1", first.MessageID, first.MessageID)
	if reply.Deliveries[0].Endpoint != b.srv.URL {
		t.Fatalf("scenario did not play: %+v", reply.Deliveries)
	}
	waitFor(t, "the reply forwarded to pod-a", func() bool { return a.forwards.Load() == 1 })
	close(release)
	waitFor(t, "pod-a's Run to end", func() bool { return a.runsEnded.Load() >= 1 })
	time.Sleep(300 * time.Millisecond)
	// pod-a may answer the forwarded message with a second card, when it was
	// left over for a Run that follows. pod-b's placeholder must be gone.
	cards := h.srv.Cards("", "", time.Time{})
	if len(cards) == 0 {
		t.Fatal("no card")
	}
	for _, c := range cards {
		if c.Instance != "pod-a" || c.Text != "done" {
			t.Fatalf("card %+v: want only pod-a's answers, no placeholder", c)
		}
	}
}
