package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/httpapi"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// fakeSender records what the bot would post to Lark.
type fakeSender struct {
	mu   sync.Mutex
	sent []sentMsg
}

type sentMsg struct{ replyTo, chat, content string }

func (f *fakeSender) Reply(_ context.Context, messageID, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sentMsg{replyTo: messageID, content: content})
	return nil
}

func (f *fakeSender) Create(_ context.Context, chatID, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sentMsg{chat: chatID, content: content})
	return nil
}

func (f *fakeSender) all() []sentMsg {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentMsg(nil), f.sent...)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// instance is one in-process bot: a Runner on the shared store, its handler on an httptest server.
type instance struct {
	runner *service.Runner
	bot    *bot
	sender *fakeSender
	srv    *httptest.Server
	model  *testkit.FuncModel
}

func logger(t *testing.T) *slog.Logger {
	return slog.New(slog.NewTextHandler(testWriter{t}, nil))
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

func newInstance(t *testing.T, store session.Store, name string, model *testkit.FuncModel) *instance {
	t.Helper()
	return newInstanceWith(t, store, name, model, nil)
}

// newInstanceWith lets a test wrap the instance's HTTP handler.
func newInstanceWith(t *testing.T, store session.Store, name string, model *testkit.FuncModel, wrap func(http.Handler) http.Handler) *instance {
	t.Helper()
	srv := httptest.NewUnstartedServer(nil)
	t.Cleanup(srv.Close)
	runner, err := service.New(service.Config{
		Store: store, Instance: name, Address: "http://" + srv.Listener.Addr().String(),
		Agents: []service.Agent{{Name: "fake", Model: model, ModelName: "fake"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeRunner(runner) })
	srv.Config.Handler = httpapi.New(runner)
	if wrap != nil {
		srv.Config.Handler = wrap(srv.Config.Handler)
	}
	srv.Start()
	snd := &fakeSender{}
	b := newBot(runner, snd, "fake", "fake", name, logger(t))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go b.replyLoop(ctx)
	return &instance{runner: runner, bot: b, sender: snd, srv: srv, model: model}
}

// gatedModel blocks its first call until release is closed.
func gatedModel(started chan<- struct{}, release <-chan struct{}) *testkit.FuncModel {
	var calls atomic.Int32
	return testkit.NewFuncModel(func(ctx context.Context, _ gotato.ModelRequest) (testkit.Script, error) {
		if calls.Add(1) == 1 {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return testkit.Text("done"), nil
	})
}

const (
	chat    = "oc_chat1"
	rootMsg = "om_root1" // the first message of the thread; the second is a reply to it
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
		_, _ = io.WriteString(w, `{"error":{"code":"not_writer"}}`)
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

// closeRunner settles a test's Runner the way Gotato documents: Drain lets every
// Run in flight finish and save, then Close removes the Liveness row. Closing
// alone leaves a Run writing into a store directory the test is about to delete.
func closeRunner(runner *service.Runner) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = runner.Drain(ctx)
	_ = runner.Close(context.Background())
}
