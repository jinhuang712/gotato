package main

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/examples/larkbot/internal/fakelark"
	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/httpapi"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func TestTwoInstancesForwardToTheWriter(t *testing.T) {
	store, err := session.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	a := newInstance(t, store, "pod-a", gatedModel(started, release))
	b := newInstance(t, store, "pod-b", testkit.NewFuncModel(func(context.Context, gotato.ModelRequest) (testkit.Script, error) {
		t.Error("instance B must not call its model")
		return testkit.Text("wrong"), nil
	}))

	ctx := context.Background()
	id := sessionIDFor(rootMsg)
	if _, err := a.runner.CreateSessionExclusive(ctx, "fake", id, map[string]string{metaChatID: chat, metaThread: rootMsg}); err != nil {
		t.Fatal(err)
	}
	// A's Run starts without A's bot seeing a Lark message, so A has no
	// message to reply to: its reply goes to the thread's root (metadata fallback).
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_, _ = a.runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: "first"})
	}()
	<-started

	// The Lark event for the same chat lands on B.
	b.bot.handle(ctx, chat, rootMsg, "om_2", "second")

	close(release)
	<-runDone
	waitFor(t, "A's reply", func() bool { return len(a.sender.all()) >= 1 })
	time.Sleep(200 * time.Millisecond) // a second post would show up by now

	got := a.sender.all()
	if len(got) != 1 {
		t.Fatalf("A posted %d messages, want 1: %+v", len(got), got)
	}
	if got[0].replyTo != rootMsg {
		t.Fatalf("A did not fall back to the thread's root message: %+v", got[0])
	}
	card := parseCard(t, got[0].content)
	if want := "session " + shortID(id) + " · instance pod-a · fake"; card.Elements[1].Elements[0].Content != want {
		t.Fatalf("footer = %q, want %q", card.Elements[1].Elements[0].Content, want)
	}
	if n := len(b.sender.all()); n != 0 {
		t.Fatalf("B posted %d messages, want 0", n)
	}
	// The forwarded text reached A's Run as a follow-up user message, after "first".
	rec, err := a.runner.Record(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var users []string
	for _, m := range rec.Messages() {
		if m.Role == gotato.RoleUser {
			users = append(users, gotato.TextOf(m))
		}
	}
	if len(users) != 2 || users[0] != "first" || users[1] != "second" {
		t.Fatalf("user messages in the Transcript = %q", users)
	}
	if b.model.Calls() != 0 {
		t.Fatal("B called its model")
	}
}

// A writer whose handler answers not_writer again must not be forwarded to a second time, and the user hears it once.
func TestForwardLoopIsNotCreated(t *testing.T) {
	var hits atomic.Int32
	var forwarded atomic.Value
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		forwarded.Store(r.Header.Get(httpapi.ForwardedHeader))
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"code":"not_writer","message":"not_writer"}`)
	}))
	defer peer.Close()
	b := &bot{log: logger(t), client: peer.Client(), label: "pod-b", model: "fake"}
	snd := &fakeSender{}
	b.send = snd
	if err := b.forward(context.Background(), peer.URL, "lark-x", "hi"); err == nil {
		t.Fatal("a second not_writer must be an error")
	}
	if hits.Load() != 1 || forwarded.Load() != "true" {
		t.Fatalf("hits=%d forwarded header=%v", hits.Load(), forwarded.Load())
	}
}

func TestFailedForwardTellsTheUserOnce(t *testing.T) {
	store, err := session.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	a := newInstance(t, store, "pod-a", gatedModel(started, release))
	b := newInstance(t, store, "pod-b", testkit.NewFuncModel(func(context.Context, gotato.ModelRequest) (testkit.Script, error) {
		return testkit.Text("wrong"), nil
	}))
	b.bot.retry = retryPolicy{} // no retry: the failed forward is told at once
	ctx := context.Background()
	id := sessionIDFor(rootMsg)
	if _, err := a.runner.CreateSessionExclusive(ctx, "fake", id, map[string]string{metaChatID: chat, metaThread: rootMsg}); err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = a.runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: "first"}) }()
	<-started
	a.srv.Close() // the writer is alive in the store, but its endpoint is gone

	b.bot.handle(ctx, chat, rootMsg, "om_2", "second")

	got := b.sender.all()
	if len(got) != 1 || got[0].replyTo != "om_2" {
		t.Fatalf("B sent %+v, want one reply to om_2", got)
	}
	if body := parseCard(t, got[0].content).Elements[0].Content; body != busyNotice {
		t.Fatalf("notice = %q", body)
	}
	if n := len(a.sender.all()); n != 0 {
		t.Fatalf("A posted %d, want 0", n)
	}
}

// larkInstance is one bot in `-lark-events http` mode: it receives events on
// /lark/event and posts cards through the Lark client pointed at the fake.
type larkInstance struct {
	runner    *service.Runner
	bot       *bot
	srv       *httptest.Server
	runsEnded atomic.Int32
	forwards  atomic.Int32 // messages other instances forwarded to this one
	ackDelay  atomic.Int64 // ns to wait before answering /lark/event (events are processed first)
}

func newLarkInstance(t *testing.T, store session.Store, name, fakeURL string, model gotato.Model) *larkInstance {
	t.Helper()
	li := &larkInstance{}
	srv := httptest.NewUnstartedServer(nil)
	t.Cleanup(srv.Close)
	runner, err := service.New(service.Config{
		Store: store, Instance: name, Address: "http://" + srv.Listener.Addr().String(),
		Agents: []service.Agent{{Name: "fake", Model: model, ModelName: "fake", Limits: botLimits()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeRunner(runner) })
	api := lark.NewClient("fake", "fake", lark.WithOpenBaseUrl(fakeURL))
	b := newBot(runner, larkSender{api}, "fake", "fake", name, logger(t))
	li.runner, li.bot, li.srv = runner, b, srv

	events := larkEventHandler(b)
	mux := http.NewServeMux()
	mux.Handle("/v1/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(httpapi.ForwardedHeader) != "" {
			li.forwards.Add(1)
		}
		httpapi.New(runner).ServeHTTP(w, r)
	}))
	mux.HandleFunc("POST /lark/event", func(w http.ResponseWriter, r *http.Request) {
		events(w, r)
		time.Sleep(time.Duration(li.ackDelay.Load()))
	})
	srv.Config.Handler = mux
	srv.Start()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go b.replyLoop(ctx)
	sub, err := runner.SubscribeInstance(ctx, service.ForAgents("fake"))
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			ev, err := sub.Next(ctx)
			if err != nil {
				return
			}
			if _, ok := ev.(gotato.RunEnded); ok {
				li.runsEnded.Add(1)
			}
		}
	}()
	return li
}

func slowModel(d time.Duration) *testkit.FuncModel {
	return testkit.NewFuncModel(func(ctx context.Context, _ gotato.ModelRequest) (testkit.Script, error) {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return testkit.Text("ok"), nil
	})
}

func userTexts(t *testing.T, r *service.Runner, id string) []string {
	t.Helper()
	rec, err := r.Record(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var users []string
	for _, m := range rec.Messages() {
		if m.Role == gotato.RoleUser {
			users = append(users, gotato.TextOf(m))
		}
	}
	return users
}

// The fake's event, decoded by the SDK's webhook adapter, reaches onMessage;
// the card comes back through the Lark client pointed at the fake.
func TestFakeEventReachesTheBotHandler(t *testing.T) {
	store, _ := session.NewFileStore(t.TempDir())
	fl, _, flStart := newFake(t)
	a := newLarkInstance(t, store, "pod-a", fl.url, slowModel(0))
	flStart(fakelark.Config{Bots: []string{a.srv.URL}})
	sent := fl.srv.Deliver(context.Background(), "oc_x", "hello", "", "")
	if d := sent.Deliveries[0]; d.Status != 200 {
		t.Fatalf("delivery: %+v", d)
	}
	waitFor(t, "the card", func() bool { return len(fl.srv.Cards("", "", time.Time{})) == 1 })
	c := fl.srv.Cards("", "", time.Time{})[0]
	if c.ReplyTo != sent.MessageID || c.Chat != "oc_x" || c.Instance != "pod-a" || c.Session != shortID(sessionIDFor(sent.MessageID)) || c.Text != "ok" {
		t.Fatalf("card = %+v", c)
	}
}

// fakeHandle is a fake Lark whose bots are not known yet: the bots need its URL, it needs theirs.
type fakeHandle struct {
	srv *fakelark.Server
	url string
	ts  *httptest.Server
}

func newFake(t *testing.T) (*fakeHandle, *httptest.Server, func(fakelark.Config)) {
	ts := httptest.NewUnstartedServer(nil)
	t.Cleanup(ts.Close)
	h := &fakeHandle{url: "http://" + ts.Listener.Addr().String(), ts: ts}
	return h, ts, func(cfg fakelark.Config) {
		h.srv = fakelark.New(cfg)
		ts.Config.Handler = h.srv.Handler()
		ts.Start()
	}
}

func burst(t *testing.T, replies int, seed int64) (fl *fakelark.Server, a, b *larkInstance, sent []fakelark.Sent, turns []string) {
	store, _ := session.NewFileStore(t.TempDir())
	h, _, start := newFake(t)
	a = newLarkInstance(t, store, "pod-a", h.url, slowModel(300*time.Millisecond))
	b = newLarkInstance(t, store, "pod-b", h.url, slowModel(300*time.Millisecond))
	start(fakelark.Config{Bots: []string{a.srv.URL, b.srv.URL}, Seed: seed})
	turns = []string{"first"}
	for i := 1; i <= replies; i++ {
		turns = append(turns, fmt.Sprintf("r%d", i))
	}
	sent, err := h.srv.Converse(context.Background(), "oc_x", turns, 20*time.Millisecond, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return h.srv, a, b, sent, turns
}

// A user replying in a thread in a burst, over two instances on one store:
// one card per Run and nothing lost.
func TestConversationBurstOverTwoInstances(t *testing.T) {
	fl, a, b, sent, turns := burst(t, 3, 7)
	for _, s := range sent {
		for _, d := range s.Deliveries {
			if d.Status != 200 {
				t.Fatalf("a delivery was not acked: %+v", s)
			}
		}
	}
	id := sessionIDFor(sent[0].MessageID)
	waitFor(t, "every user message in the Transcript", func() bool { return len(userTexts(t, a.runner, id)) == len(turns) })
	runs := func() int { return int(a.runsEnded.Load() + b.runsEnded.Load()) }
	waitFor(t, "a card per Run", func() bool {
		time.Sleep(400 * time.Millisecond)
		return runs() >= 2 && len(fl.Cards("", "", time.Time{})) == runs()
	})

	seen := map[string]int{}
	for _, u := range userTexts(t, a.runner, id) {
		seen[u]++
	}
	for _, want := range turns {
		if seen[want] != 1 {
			t.Fatalf("%q appears %d times in the Transcript: %v", want, seen[want], seen)
		}
	}
	// Whether a reply meets not_writer depends on timing: an instance whose
	// message finds the Session between two Runs takes it over instead. The
	// forward is checked in TestReplyOnTheInstanceWithoutTheRunIsForwarded.
	t.Logf("runs=%d cards=%d forwards a=%d b=%d", runs(), len(fl.Cards("", "", time.Time{})), a.forwards.Load(), b.forwards.Load())
}

// A reply that lands on the instance without the Run is forwarded to the
// writer. The writer's Run is held in flight by a model the test releases, so
// the reply meets not_writer whatever the timing.
func TestReplyOnTheInstanceWithoutTheRunIsForwarded(t *testing.T) {
	store, _ := session.NewFileStore(t.TempDir())
	h, _, start := newFake(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	gated := testkit.NewFuncModel(func(ctx context.Context, _ gotato.ModelRequest) (testkit.Script, error) {
		once.Do(func() { close(entered) })
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return testkit.Text("ok"), nil
	})
	a := newLarkInstance(t, store, "pod-a", h.url, gated)
	b := newLarkInstance(t, store, "pod-b", h.url, slowModel(0))
	start(fakelark.Config{Bots: []string{a.srv.URL, b.srv.URL}, Seed: seedPicking(2, 0, 1)})

	first := h.srv.Deliver(context.Background(), "oc_x", "first", "", "")
	<-entered // pod-a's Run is in flight and stays so until release
	reply := h.srv.Deliver(context.Background(), "oc_x", "r1", first.MessageID, first.MessageID)
	if first.Deliveries[0].Endpoint != a.srv.URL || reply.Deliveries[0].Endpoint != b.srv.URL {
		t.Fatalf("scenario did not play: %+v %+v", first.Deliveries, reply.Deliveries)
	}
	waitFor(t, "the reply forwarded to pod-a", func() bool { return a.forwards.Load() == 1 })
	close(release)

	id := sessionIDFor(first.MessageID)
	waitFor(t, "both messages in the Transcript", func() bool { return len(userTexts(t, a.runner, id)) == 2 })
	users := userTexts(t, a.runner, id)
	if users[0] != "first" || users[1] != "r1" || b.forwards.Load() != 0 {
		t.Fatalf("transcript %q, forwards to pod-b %d", users, b.forwards.Load())
	}
	waitFor(t, "a card per Run", func() bool {
		return a.runsEnded.Load() >= 1 && len(h.srv.Cards("", "", time.Time{})) == int(a.runsEnded.Load()+b.runsEnded.Load())
	})
}

// A burst longer than Gotato's default FollowUp buffer of 4 is kept whole: the
// bot raises the buffer (botLimits), so no message is refused with limit_exceeded.
func TestBurstBeyondTheDefaultFollowUpBufferLosesNothing(t *testing.T) {
	_, a, b, sent, turns := burst(t, 9, 7)
	id := sessionIDFor(sent[0].MessageID)
	// Every message is committed by a Run that may still be going, so wait for
	// the count instead of for a time; a message that is lost never arrives.
	waitFor(t, "every user message in the Transcript", func() bool { return len(userTexts(t, a.runner, id)) == len(turns) })
	users := userTexts(t, a.runner, id)
	t.Logf("sent %d, Transcript holds %d, runs=%d", len(turns), len(users), a.runsEnded.Load()+b.runsEnded.Load())
}

// seedPicking returns a seed whose first picks over n endpoints are want.
func seedPicking(n int, want ...int) int64 {
	for seed := int64(1); ; seed++ {
		r := rand.New(rand.NewSource(seed))
		ok := true
		for _, w := range want {
			if r.Intn(n) != w {
				ok = false
				break
			}
		}
		if ok {
			return seed
		}
	}
}

// The dedup gap, as it stands: the seen set is per instance, so a redelivery
// (same event, same message_id) that lands on the other instance is handled
// again. This test documents today's behavior; it is not a goal.
func TestRedeliveryToAnotherInstanceCurrentlyDuplicates(t *testing.T) {
	setup := func(t *testing.T, model func() gotato.Model, picks ...int) (fl *fakelark.Server, a, b *larkInstance) {
		store, _ := session.NewFileStore(t.TempDir())
		h, _, start := newFake(t)
		a = newLarkInstance(t, store, "pod-a", h.url, model())
		b = newLarkInstance(t, store, "pod-b", h.url, model())
		a.ackDelay.Store(int64(400 * time.Millisecond)) // A processes the event but acks late
		start(fakelark.Config{Bots: []string{a.srv.URL, b.srv.URL}, Redeliver: true, MaxRedeliver: 1,
			AckTimeout: 150 * time.Millisecond, Seed: seedPicking(2, picks...)})
		return h.srv, a, b
	}

	t.Run("run still going: the text is queued into the Run twice", func(t *testing.T) {
		fl, a, b := setup(t, func() gotato.Model { return slowModel(1500 * time.Millisecond) }, 0, 1)
		sent := fl.Deliver(context.Background(), "oc_x", "hello", "", "")
		if sent.Redeliveries() != 1 || sent.Deliveries[0].Endpoint == sent.Deliveries[1].Endpoint {
			t.Fatalf("scenario did not play: %+v", sent.Deliveries)
		}
		id := sessionIDFor(sent.MessageID)
		waitFor(t, "the Run to end", func() bool { return a.runsEnded.Load()+b.runsEnded.Load() >= 1 })
		waitFor(t, "the text in the Transcript twice", func() bool { return len(userTexts(t, a.runner, id)) >= 2 })
		time.Sleep(500 * time.Millisecond) // a further Run or card would show up by now
		users := userTexts(t, a.runner, id)
		cards := fl.Cards("", "", time.Time{})
		t.Logf("runs=%d cards=%d transcript=%q", a.runsEnded.Load()+b.runsEnded.Load(), len(cards), users)
		if len(users) != 2 || users[0] != "hello" || users[1] != "hello" {
			t.Fatalf("the redelivered text should be in the Transcript twice today: %q", users)
		}
		if a.runsEnded.Load()+b.runsEnded.Load() != 1 || len(cards) != 1 {
			t.Fatalf("expected one Run and one card (the duplicate is steered into it): runs=%d cards=%d", a.runsEnded.Load()+b.runsEnded.Load(), len(cards))
		}
	})

	t.Run("run already ended: a second Run and a second card", func(t *testing.T) {
		fl, a, b := setup(t, func() gotato.Model { return slowModel(0) }, 0, 1)
		sent := fl.Deliver(context.Background(), "oc_x", "hello", "", "")
		if sent.Redeliveries() != 1 || sent.Deliveries[0].Endpoint == sent.Deliveries[1].Endpoint {
			t.Fatalf("scenario did not play: %+v", sent.Deliveries)
		}
		id := sessionIDFor(sent.MessageID)
		waitFor(t, "both cards", func() bool { return len(fl.Cards("", "", time.Time{})) >= 2 })
		waitFor(t, "the text in the Transcript twice", func() bool { return len(userTexts(t, a.runner, id)) >= 2 })
		time.Sleep(300 * time.Millisecond) // a third card would show up by now
		cards := fl.Cards("", "", time.Time{})
		users := userTexts(t, a.runner, id)
		t.Logf("runs=%d cards=%d transcript=%q", a.runsEnded.Load()+b.runsEnded.Load(), len(cards), users)
		if len(cards) != 2 || len(users) != 2 {
			t.Fatalf("expected a duplicate Run today: cards=%d transcript=%q", len(cards), users)
		}
	})

	t.Run("the same instance catches the redelivery", func(t *testing.T) {
		fl, a, b := setup(t, func() gotato.Model { return slowModel(0) }, 0, 0)
		sent := fl.Deliver(context.Background(), "oc_x", "hello", "", "")
		if sent.Redeliveries() != 1 || sent.Deliveries[0].Endpoint != sent.Deliveries[1].Endpoint {
			t.Fatalf("scenario did not play: %+v", sent.Deliveries)
		}
		waitFor(t, "the card", func() bool { return len(fl.Cards("", "", time.Time{})) >= 1 })
		time.Sleep(400 * time.Millisecond)
		if n := len(fl.Cards("", "", time.Time{})); n != 1 {
			t.Fatalf("cards = %d, want 1", n)
		}
		if got := userTexts(t, b.runner, sessionIDFor(sent.MessageID)); len(got) != 1 {
			t.Fatalf("transcript = %q", got)
		}
		_ = a
	})
}

// -ack-delay holds the event's answer, and the message is handled at once all the same.
func TestAckDelayHoldsTheAnswerNotTheWork(t *testing.T) {
	store, _ := session.NewFileStore(t.TempDir())
	fl, _, flStart := newFake(t)
	a := newLarkInstance(t, store, "pod-a", fl.url, slowModel(0))
	a.bot.ackDelay = 600 * time.Millisecond
	flStart(fakelark.Config{Bots: []string{a.srv.URL}, Redeliver: false})
	sent := fl.srv.Deliver(context.Background(), "oc_x", "hello", "", "")
	d := sent.Deliveries[0]
	if d.Status != 200 || d.Millis < 600 {
		t.Fatalf("delivery: %+v", d)
	}
	waitFor(t, "the card", func() bool { return len(fl.srv.Cards("", "", time.Time{})) == 1 })
}
