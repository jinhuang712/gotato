package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// releaseGate holds the write that releases the Session, once, so a Run is held
// after run_ended, when it takes no message and still owns the Session.
type releaseGate struct {
	session.Store
	reached chan struct{}
	proceed chan struct{}
	once    sync.Once
}

func (g *releaseGate) Append(ctx context.Context, id string, after uint64, entries []session.Entry, summary session.RecordInfo) error {
	for _, entry := range entries {
		if entry.Kind == "session_released" {
			g.once.Do(func() {
				close(g.reached)
				<-g.proceed
			})
		}
	}
	return g.Store.Append(ctx, id, after, entries, summary)
}

// A message posted after run_ended and before the Run releases the Session
// waits for the release and runs in a following Run, instead of answering busy.
func TestMessageWhileARunIsEndingRunsInTheNextRun(t *testing.T) {
	gate := &releaseGate{Store: session.NewMemoryStore(), reached: make(chan struct{}), proceed: make(chan struct{})}
	a := newInstance(t, gate, "instance-a", testkit.EchoModel{}, false)
	s, err := a.runner.CreateSession(context.Background(), "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_, _ = a.runner.Run(context.Background(), service.RunRequest{SessionID: s.ID(), Prompt: "hello"})
	}()
	select {
	case <-gate.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the Run never reached its release")
	}
	type answer struct {
		status int
		body   []byte
	}
	answered := make(chan answer, 1)
	go func() {
		resp, body := a.post(t, "/v1/sessions/"+s.ID()+"/messages", map[string]string{"kind": "follow_up", "text": "again"})
		answered <- answer{resp.StatusCode, body}
	}()
	select {
	case got := <-answered:
		t.Fatalf("the message was answered before the release: %d %s", got.status, got.body)
	case <-time.After(50 * time.Millisecond):
	}
	close(gate.proceed)
	select {
	case got := <-answered:
		var out struct {
			Queued bool `json:"queued"`
			Run    *struct {
				FinalText string `json:"final_text"`
			} `json:"run"`
		}
		if err := json.Unmarshal(got.body, &out); err != nil || got.status != http.StatusOK || out.Queued || out.Run == nil || out.Run.FinalText != "echo: again" {
			t.Fatalf("message = %d %s, want 200 with a following Run answering \"echo: again\"", got.status, got.body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the message never answered")
	}
}
