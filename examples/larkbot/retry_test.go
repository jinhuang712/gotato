package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato/gotato-core"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/httpapi"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

var fastRetry = retryPolicy{budget: 5 * time.Second, first: 10 * time.Millisecond, max: 40 * time.Millisecond}

func echoText(text string) *testkit.FuncModel {
	return testkit.NewFuncModel(func(context.Context, gotato.ModelRequest) (testkit.Script, error) { return testkit.Text(text), nil })
}

// deadWriter sets up instance A as the writer of a Session (a Run held open), with its endpoint
// gone: from B, A is alive in the store and unreachable, as a pod that was just killed is until
// its Liveness row expires. Closing release ends A's Run, so B may take the Session over.
func deadWriter(t *testing.T, wrap func(http.Handler) http.Handler) (a, b *instance, id string, release chan struct{}, aDone chan struct{}) {
	t.Helper()
	store, err := session.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release = make(chan struct{})
	a = newInstanceWith(t, store, "pod-a", gatedModel(started, release), wrap)
	b = newInstance(t, store, "pod-b", echoText("from b"))
	b.bot.retry = fastRetry
	ctx := context.Background()
	id = sessionIDFor(rootMsg)
	if _, err := a.runner.CreateSessionExclusive(ctx, "fake", id, map[string]string{metaChatID: chat, metaThread: rootMsg}); err != nil {
		t.Fatal(err)
	}
	aDone = make(chan struct{})
	go func() {
		defer close(aDone)
		_, _ = a.runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: "first"})
	}()
	<-started
	return a, b, id, release, aDone
}

func cardBodies(t *testing.T, s *fakeSender) []string {
	t.Helper()
	var out []string
	for _, m := range s.all() {
		out = append(out, parseCard(t, m.content).Elements[0].Content)
	}
	return out
}

// The writer's endpoint is dead for the first attempts; then the writer is gone and B takes the
// Session over: the message is delivered after a retry, and the user gets one reply.
func TestRetryDeliversAfterTheWriterIsGone(t *testing.T) {
	a, b, id, release, aDone := deadWriter(t, nil)
	a.srv.Close()
	done := make(chan struct{})
	go func() { defer close(done); b.bot.handle(context.Background(), chat, rootMsg, "om_2", "second") }()
	time.Sleep(150 * time.Millisecond) // several refused attempts
	select {
	case <-done:
		t.Fatal("the message finished while the writer was still there")
	default:
	}
	close(release)
	<-aDone
	<-done
	waitFor(t, "B's reply", func() bool { return len(b.sender.all()) >= 1 })
	time.Sleep(100 * time.Millisecond)

	got := cardBodies(t, b.sender)
	if len(got) != 1 || got[0] != "from b" {
		t.Fatalf("B sent %q, want one reply", got)
	}
	if users := userTexts(t, b.runner, id); len(users) != 2 || users[1] != "second" {
		t.Fatalf("user messages = %q", users)
	}
}

// The budget runs out: the user is told once, and the attempts were many.
func TestRetryBudgetExhaustedTellsTheUserOnce(t *testing.T) {
	a, b, _, release, _ := deadWriter(t, nil)
	defer close(release)
	a.srv.Close()
	b.bot.retry = retryPolicy{budget: 300 * time.Millisecond, first: 10 * time.Millisecond, max: 40 * time.Millisecond}
	start := time.Now()
	b.bot.handle(context.Background(), chat, rootMsg, "om_2", "second")
	if d := time.Since(start); d < 200*time.Millisecond || d > 3*time.Second {
		t.Fatalf("handle took %v, want about the budget", d)
	}
	got := b.sender.all()
	if len(got) != 1 || got[0].replyTo != "om_2" || cardBodies(t, b.sender)[0] != busyNotice {
		t.Fatalf("B sent %+v, want one busy card", got)
	}
}

// A connection that was made and then lost may have delivered the message: no retry.
func TestRetryNotForAmbiguousFailure(t *testing.T) {
	var hits atomic.Int32
	a, b, _, release, _ := deadWriter(t, func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			// Read the request, then drop the connection.
			if h, ok := w.(http.Hijacker); ok {
				if c, _, err := h.Hijack(); err == nil {
					_ = c.Close()
				}
			}
		})
	})
	defer close(release)
	_ = a
	b.bot.handle(context.Background(), chat, rootMsg, "om_2", "second")
	if hits.Load() != 1 {
		t.Fatalf("forwarded %d times, want 1", hits.Load())
	}
	if got := cardBodies(t, b.sender); len(got) != 1 || got[0] != busyNotice {
		t.Fatalf("B sent %q", got)
	}
}

// A 409 that says "not run" is retried; a 500 is not.
func TestForwardClassifiesFailures(t *testing.T) {
	var status atomic.Int64
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status.Load() == http.StatusConflict {
			w.Header().Set(httpapi.NotRunHeader, "true")
		}
		w.WriteHeader(int(status.Load()))
	}))
	b := &bot{log: logger(t), client: peer.Client()}
	for _, c := range []struct {
		status int
		notRun bool
	}{{http.StatusConflict, true}, {http.StatusServiceUnavailable, false}, {http.StatusInternalServerError, false}} {
		status.Store(int64(c.status))
		if err := b.forward(context.Background(), peer.URL, "s", "x"); err == nil || notRun(err) != c.notRun {
			t.Fatalf("status %d: err=%v notRun=%v, want %v", c.status, err, err != nil && notRun(err), c.notRun)
		}
	}
	peer.Close() // nothing listens any more: a dial failure
	if err := b.forward(context.Background(), peer.URL, "s", "x"); err == nil || !notRun(err) {
		t.Fatalf("dial failure: err=%v, want not run", err)
	}
}

// Two messages of one Session while the first retries: the second does not overtake the first.
func TestRetryKeepsOrder(t *testing.T) {
	a, b, id, release, aDone := deadWriter(t, nil)
	a.srv.Close()
	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); b.bot.handle(ctx, chat, rootMsg, "om_2", "second") }()
	time.Sleep(80 * time.Millisecond)
	go func() { defer wg.Done(); b.bot.handle(ctx, chat, rootMsg, "om_3", "third") }()
	time.Sleep(80 * time.Millisecond)
	close(release)
	<-aDone
	wg.Wait()
	waitFor(t, "two replies", func() bool { return len(b.sender.all()) >= 2 })

	users := userTexts(t, b.runner, id)
	if len(users) != 3 || users[1] != "second" || users[2] != "third" {
		t.Fatalf("user messages = %q, want first, second, third", users)
	}
}
