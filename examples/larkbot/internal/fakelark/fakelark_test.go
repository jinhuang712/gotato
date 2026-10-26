package fakelark

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// bot is a fake bot endpoint that records the events it gets.
type bot struct {
	srv    *httptest.Server
	mu     sync.Mutex
	events []map[string]any
	delay  time.Duration // sleep before answering
	hits   atomic.Int32
}

func newBot(t *testing.T, delay time.Duration) *bot {
	t.Helper()
	b := &bot{delay: delay}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != EventPath {
			http.NotFound(w, r)
			return
		}
		var ev map[string]any
		_ = json.NewDecoder(r.Body).Decode(&ev)
		b.mu.Lock()
		b.events = append(b.events, ev)
		b.mu.Unlock()
		b.hits.Add(1)
		time.Sleep(b.delay)
		w.WriteHeader(200)
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func post(t *testing.T, url string, body any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	b, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(b, &out)
	return resp.StatusCode, out
}

const sampleCard = `{"config":{"wide_screen_mode":true},"header":{"template":"green","title":{"tag":"plain_text","content":"demo · pod-a"}},"elements":[{"tag":"markdown","content":"hello"},{"tag":"note","elements":[{"tag":"plain_text","content":"session om_root1 · instance pod-a · demo"}]}]}`

func TestAPIShapesAndCardRecording(t *testing.T) {
	b := newBot(t, 0)
	s := New(Config{Bots: []string{b.srv.URL}})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	code, tok := post(t, ts.URL+"/open-apis/auth/v3/tenant_access_token/internal", map[string]string{"app_id": "fake", "app_secret": "fake"})
	if code != 200 || tok["code"] != float64(0) || tok["tenant_access_token"] == "" || tok["expire"] == nil {
		t.Fatalf("token: %d %v", code, tok)
	}
	// A message the fake delivered is known, so a reply to it carries its chat.
	sent := s.Deliver(context.Background(), "oc_x", "hi", "", "")
	code, rep := post(t, ts.URL+"/open-apis/im/v1/messages/"+sent.MessageID+"/reply", map[string]string{"msg_type": "interactive", "content": sampleCard})
	data, _ := rep["data"].(map[string]any)
	if code != 200 || rep["code"] != float64(0) || rep["msg"] != "success" || !strings.HasPrefix(data["message_id"].(string), "om_fake_") {
		t.Fatalf("reply: %d %v", code, rep)
	}
	code, cr := post(t, ts.URL+"/open-apis/im/v1/messages?receive_id_type=chat_id", map[string]string{"receive_id": "oc_y", "msg_type": "interactive", "content": sampleCard})
	if code != 200 || cr["code"] != float64(0) {
		t.Fatalf("create: %d %v", code, cr)
	}

	cards := s.Cards("", "", time.Time{})
	if len(cards) != 2 {
		t.Fatalf("recorded %d cards", len(cards))
	}
	c := cards[0]
	if c.Chat != "oc_x" || c.ReplyTo != sent.MessageID || c.Session != "om_root1" || c.Instance != "pod-a" || c.Text != "hello" {
		t.Fatalf("card = %+v", c)
	}
	if cards[1].Chat != "oc_y" || cards[1].ReplyTo != "" {
		t.Fatalf("created card = %+v", cards[1])
	}
	if got := s.Cards("oc_y", "", time.Time{}); len(got) != 1 {
		t.Fatalf("chat filter: %d", len(got))
	}
	if got := s.Cards("", sent.MessageID, time.Now().Add(time.Hour)); len(got) != 0 {
		t.Fatalf("since filter: %d", len(got))
	}
	s.Reset()
	if len(s.Cards("", "", time.Time{})) != 0 {
		t.Fatal("reset kept cards")
	}
}

func TestEventEnvelopeShape(t *testing.T) {
	b := newBot(t, 0)
	s := New(Config{Bots: []string{b.srv.URL}})
	s.Deliver(context.Background(), "oc_x", "hello", "om_root", "om_card")
	b.mu.Lock()
	ev := b.events[0]
	b.mu.Unlock()
	hdr := ev["header"].(map[string]any)
	msg := ev["event"].(map[string]any)["message"].(map[string]any)
	if ev["schema"] != "2.0" || hdr["event_type"] != "im.message.receive_v1" || hdr["event_id"] == "" {
		t.Fatalf("header: %v", ev)
	}
	if msg["root_id"] != "om_root" || msg["parent_id"] != "om_card" || msg["chat_id"] != "oc_x" || msg["message_type"] != "text" || msg["content"] != `{"text":"hello"}` {
		t.Fatalf("message: %v", msg)
	}
}

func TestSendDeliversCountMessages(t *testing.T) {
	b := newBot(t, 0)
	ts := httptest.NewServer(New(Config{Bots: []string{b.srv.URL}}).Handler())
	defer ts.Close()
	code, out := post(t, ts.URL+"/send", map[string]any{"chat": "oc_x", "text": "hi", "count": 4, "parallel": true})
	if code != 200 || len(out["sent"].([]any)) != 4 || b.hits.Load() != 4 {
		t.Fatalf("send: %d %v hits=%d", code, out, b.hits.Load())
	}
}

func TestRedeliversASlowAckToARandomEndpoint(t *testing.T) {
	slow, fast := newBot(t, 400*time.Millisecond), newBot(t, 0)
	s := New(Config{Bots: []string{slow.srv.URL, fast.srv.URL}, Redeliver: true, AckTimeout: 100 * time.Millisecond, MaxRedeliver: 30, Seed: 1})
	for i := range 8 {
		got := s.Deliver(context.Background(), "oc_x", "hi", "", "")
		last := got.Deliveries[len(got.Deliveries)-1]
		if last.Status != 200 || last.Endpoint != fast.srv.URL {
			t.Fatalf("delivery %d ended %+v", i, got.Deliveries)
		}
		for _, d := range got.Deliveries[:len(got.Deliveries)-1] {
			if d.Endpoint != slow.srv.URL || d.Status != 0 {
				t.Fatalf("a failed attempt should be a timeout on the slow bot: %+v", got.Deliveries)
			}
		}
	}
	// Every redelivery carries the same event.
	slow.mu.Lock()
	defer slow.mu.Unlock()
	if len(slow.events) == 0 {
		t.Fatal("slow bot was never picked")
	}
}

func TestRedeliverOffDeliversOnce(t *testing.T) {
	slow := newBot(t, 300*time.Millisecond)
	s := New(Config{Bots: []string{slow.srv.URL}, AckTimeout: 50 * time.Millisecond})
	got := s.Deliver(context.Background(), "oc_x", "hi", "", "")
	if len(got.Deliveries) != 1 || got.Redeliveries() != 0 {
		t.Fatalf("deliveries = %+v", got.Deliveries)
	}
}

func TestRedeliveryStopsAfterMax(t *testing.T) {
	slow := newBot(t, 300*time.Millisecond)
	s := New(Config{Bots: []string{slow.srv.URL}, Redeliver: true, AckTimeout: 30 * time.Millisecond, MaxRedeliver: 3})
	got := s.Deliver(context.Background(), "oc_x", "hi", "", "")
	if len(got.Deliveries) != 4 {
		t.Fatalf("want 1 delivery + 3 redeliveries, got %d", len(got.Deliveries))
	}
	ids := map[string]bool{}
	slow.mu.Lock()
	defer slow.mu.Unlock()
	for _, ev := range slow.events {
		ids[ev["header"].(map[string]any)["event_id"].(string)] = true
	}
	if len(ids) != 1 {
		t.Fatalf("redeliveries must keep the event_id: %v", ids)
	}
}

func TestRandomChoiceCoversAllEndpointsAndIsSeeded(t *testing.T) {
	bots := []*bot{newBot(t, 0), newBot(t, 0), newBot(t, 0)}
	urls := []string{bots[0].srv.URL, bots[1].srv.URL, bots[2].srv.URL}
	run := func(seed int64) []string {
		s := New(Config{Bots: urls, Seed: seed})
		var order []string
		for range 60 {
			order = append(order, s.Deliver(context.Background(), "oc_x", "hi", "", "").Deliveries[0].Endpoint)
		}
		return order
	}
	a, b := run(42), run(42)
	seen := map[string]int{}
	for i := range a {
		seen[a[i]]++
		if a[i] != b[i] {
			t.Fatal("same seed, different choices")
		}
	}
	if len(seen) != 3 {
		t.Fatalf("endpoints hit: %v", seen)
	}
}

func TestBotsDNSIsResolvedAtEveryDelivery(t *testing.T) {
	b := newBot(t, 0)
	host := strings.TrimPrefix(b.srv.URL, "http://")
	_, port, _ := strings.Cut(host, ":")
	var calls atomic.Int32
	s := New(Config{BotsDNS: "bots.svc:" + port, Resolver: func(_ context.Context, name string) ([]string, error) {
		calls.Add(1)
		if name != "bots.svc" {
			t.Errorf("resolved %q", name)
		}
		return []string{"127.0.0.1"}, nil
	}})
	s.Deliver(context.Background(), "oc_x", "a", "", "")
	s.Deliver(context.Background(), "oc_x", "b", "", "")
	if calls.Load() != 2 || b.hits.Load() != 2 {
		t.Fatalf("resolves=%d hits=%d", calls.Load(), b.hits.Load())
	}
}

func TestConversationRepliesInsideTheThread(t *testing.T) {
	b := newBot(t, 0)
	s := New(Config{Bots: []string{b.srv.URL}})
	// A scripted bot: the card for the first message appears shortly after.
	go func() {
		for len(s.msgChatSnapshot()) == 0 {
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(50 * time.Millisecond)
		first := s.firstMessage()
		s.mu.Lock()
		s.cards = append(s.cards, Card{Time: time.Now(), MessageID: "om_card1", Chat: "oc_x", ReplyTo: first})
		s.mu.Unlock()
	}()
	res, err := s.Converse(context.Background(), "oc_x", []string{"one", "two", "three"}, 5*time.Millisecond, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 3 || res[1].RootID != res[0].MessageID || res[2].RootID != res[0].MessageID || res[1].ParentID != "om_card1" {
		t.Fatalf("thread: %+v", res)
	}
}

func (s *Server) msgChatSnapshot() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	maps.Copy(out, s.msgChat)
	return out
}

func (s *Server) firstMessage() string {
	for k := range s.msgChatSnapshot() {
		return k
	}
	return ""
}
