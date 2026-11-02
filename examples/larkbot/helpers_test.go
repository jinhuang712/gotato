package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	httpapi "github.com/jinhuang712/gotato/adapter/http"
	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/host"
	"github.com/jinhuang712/gotato/profile"
	"github.com/jinhuang712/gotato/store"
	"github.com/jinhuang712/gotato/testkit"
)

// fakeSender keeps what the bot would have on screen in Lark: a card that is
// updated shows its new content, and one that is recalled is gone.
type fakeSender struct {
	mu   sync.Mutex
	sent []sentMsg
	next int
}

type sentMsg struct{ id, replyTo, chat, content string }

func (f *fakeSender) add(m sentMsg) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	m.id = fmt.Sprintf("om_card_%d", f.next)
	f.sent = append(f.sent, m)
	return m.id
}

func (f *fakeSender) Reply(_ context.Context, messageID, content string) (string, error) {
	return f.add(sentMsg{replyTo: messageID, content: content}), nil
}

func (f *fakeSender) Create(_ context.Context, chatID, content string) (string, error) {
	return f.add(sentMsg{chat: chatID, content: content}), nil
}

func (f *fakeSender) Update(_ context.Context, cardID, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.sent {
		if f.sent[i].id == cardID {
			f.sent[i].content = content
		}
	}
	return nil
}

func (f *fakeSender) Delete(_ context.Context, cardID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = slices.DeleteFunc(f.sent, func(m sentMsg) bool { return m.id == cardID })
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

// storeParts is what a MemoryStore or a FileStore is: both halves of a Store.
type storeParts interface {
	store.Records
	store.Liveness
}

// memStore is a Store of one MemoryStore.
func memStore() store.Store {
	m := store.NewMemoryStore()
	return store.Store{Records: m, Liveness: m}
}

// instance is one in-process bot: a Runner on the shared store, its handler on an httptest server.
type instance struct {
	runner *host.Runner
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

func newInstance(t *testing.T, store storeParts, name string, model *testkit.FuncModel) *instance {
	t.Helper()
	return newInstanceWith(t, store, name, model, nil)
}

// newInstanceWith lets a test wrap the instance's HTTP handler.
func newInstanceWith(t *testing.T, st storeParts, name string, model *testkit.FuncModel, wrap func(http.Handler) http.Handler) *instance {
	t.Helper()
	srv := httptest.NewUnstartedServer(nil)
	t.Cleanup(srv.Close)
	runner, err := host.New(host.Config{
		Store: store.Store{Records: st, Liveness: st}, Instance: name, Address: "http://" + srv.Listener.Addr().String(),
		Profiles: registryOf(profile.AgentProfile{Name: "fake", Model: model, ModelName: "fake"}), DefaultAgent: "fake",
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
	b := newBot(runner, st, snd, "fake", "fake", name, logger(t))
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

// closeRunner settles a test's Runner the way Gotato documents: Drain lets every
// Run in flight finish and save, then Close removes the Liveness row. Closing
// alone leaves a Run writing into a store directory the test is about to delete.
func closeRunner(runner *host.Runner) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = runner.Drain(ctx)
	_ = runner.Close(context.Background())
}

type testCard struct {
	Header struct {
		Template string
		Title    struct{ Content string }
	}
	Elements []struct {
		Tag      string
		Content  string
		Elements []struct{ Tag, Content string }
	}
}

func parseCard(t *testing.T, content string) testCard {
	t.Helper()
	var c testCard
	if err := json.Unmarshal([]byte(content), &c); err != nil {
		t.Fatalf("card is not JSON: %v", err)
	}
	return c
}
