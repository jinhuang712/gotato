package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/service/httpapi"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

type noticeAnswer struct {
	SessionID string `json:"session_id"`
	Number    uint64 `json:"number"`
	MessageID string `json:"message_id"`
}

// A notice sent to a Run is answered 202 with the number of its notice_queued
// Event; it starts no Run, and goes in right before the model call of the
// Session's next Run, after its prompt.
func TestNoticeIsQueuedForTheNextModelCall(t *testing.T) {
	g := newGate()
	a := newInstance(t, session.NewMemoryStore(), "instance-a", g, true)
	id, wait := running(t, a, g)

	resp, body := a.post(t, "/v1/sessions/"+id+"/notices", map[string]string{"kind": "order_update", "text": "Order 42 was delivered."})
	var answer noticeAnswer
	if err := json.Unmarshal(body, &answer); err != nil || resp.StatusCode != http.StatusAccepted || answer.Number == 0 || answer.MessageID == "" || answer.SessionID != id {
		t.Fatalf("notice = %d %s, want 202 with a number", resp.StatusCode, body)
	}
	close(g.release)
	wait()
	// The Run settled without it: a notice keeps no Run going.
	if got := transcript(t, a, id); got != "user:hello|assistant:reply to hello" {
		t.Fatalf("transcript = %q", got)
	}
	resp, body = a.post(t, "/v1/sessions/"+id+"/runs", map[string]string{"prompt": "again"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("run = %d %s", resp.StatusCode, body)
	}
	if got := transcript(t, a, id); !strings.Contains(got, "user:again|notice:Order 42 was delivered.|assistant:") {
		t.Fatalf("transcript = %q, want the notice after the prompt", got)
	}
}

func TestNoticeValidation(t *testing.T) {
	a := newInstance(t, session.NewMemoryStore(), "instance-a", testkit.EchoModel{}, false)
	s, _ := a.runner.CreateSession(context.Background(), "echo", nil)
	for name, body := range map[string]map[string]string{
		"no kind":       {"text": "hi"},
		"a reserved":    {"kind": "time_away", "text": "hi"},
		"no text":       {"kind": "k"},
		"a blank text ": {"kind": "k", "text": "  "},
	} {
		resp, out := a.post(t, "/v1/sessions/"+s.ID()+"/notices", body)
		if resp.StatusCode != http.StatusBadRequest || resp.Header.Get(httpapi.NotRunHeader) != "true" {
			t.Fatalf("%s: %d %s, want 400, not run", name, resp.StatusCode, out)
		}
	}
	if resp, _ := a.post(t, "/v1/sessions/missing/notices", map[string]string{"kind": "k", "text": "hi"}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a notice for a missing Session = %d, want 404", resp.StatusCode)
	}
}

func TestNoticeForARunElsewhereAnswersNotWriterOrIsForwarded(t *testing.T) {
	store := session.NewMemoryStore()
	g := newGate()
	a := newInstance(t, store, "instance-a", g, true)
	plain := newInstance(t, store, "instance-b", testkit.EchoModel{}, true)
	forwarding := newInstance(t, store, "instance-c", testkit.EchoModel{}, true, httpapi.WithForwarding(nil, anyAddress))
	id, wait := running(t, a, g)

	resp, body := plain.post(t, "/v1/sessions/"+id+"/notices", map[string]string{"kind": "k", "text": "from b"})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), `"code":"not_writer"`) || resp.Header.Get(httpapi.WriterHeader) != a.server.URL || resp.Header.Get(httpapi.NotRunHeader) != "true" {
		t.Fatalf("%d %v %s, want 409 not_writer naming the writer", resp.StatusCode, resp.Header, body)
	}
	before := a.requests.Load()
	resp, body = forwarding.post(t, "/v1/sessions/"+id+"/notices", map[string]string{"kind": "k", "text": "via c"})
	if resp.StatusCode != http.StatusAccepted || !strings.Contains(string(body), `"number":`) || a.requests.Load() != before+1 {
		t.Fatalf("forwarded notice = %d %s (%d requests at the writer)", resp.StatusCode, body, a.requests.Load()-before)
	}
	close(g.release)
	wait()
}
