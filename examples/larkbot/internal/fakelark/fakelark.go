// Package fakelark plays Lark for the bot: it serves the subset of the Open
// API the bot calls (token, reply, create message), records every card, and
// delivers fabricated im.message.receive_v1 events to bot endpoints with
// Lark's cluster semantics (one random endpoint per delivery, redelivery when
// the ack takes more than 3 seconds). Standard library only.
package fakelark

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// EventPath is where a bot endpoint receives events (bot flag -lark-events http).
const EventPath = "/lark/event"

// Config configures a Server.
type Config struct {
	Bots         []string // bot base URLs, e.g. http://127.0.0.1:8080
	BotsDNS      string   // name:port resolved at every delivery (a headless service); used when Bots is empty
	Redeliver    bool     // redeliver an event whose ack is late or not 2xx
	AckTimeout   time.Duration
	MaxRedeliver int           // redeliveries after the first delivery
	RetryDelay   time.Duration // wait before a redelivery (Lark waits longer; tests keep it 0)
	ThinkTime    time.Duration // a user's pause between seeing a card and replying in /conversation (default 100ms)
	SeqStart     int           // the first message and event IDs count from here (a restart on a kept store must not reuse IDs)
	Seed         int64         // seeds the endpoint choice; 0 means time-based
	Resolver     func(ctx context.Context, host string) ([]string, error)
}

// Card is one message the bot posted.
type Card struct {
	Time      time.Time       `json:"time"`
	MessageID string          `json:"message_id"`
	Chat      string          `json:"chat"`
	ReplyTo   string          `json:"reply_to,omitempty"`
	Card      json.RawMessage `json:"card"`
	Text      string          `json:"text"`
	Session   string          `json:"session,omitempty"`
	Instance  string          `json:"instance,omitempty"`
}

// Delivery is one attempt to hand an event to a bot endpoint.
type Delivery struct {
	Endpoint string `json:"endpoint"`
	Status   int    `json:"status"`
	Error    string `json:"error,omitempty"`
	Millis   int64  `json:"ms"`
}

// Sent is what /send reports for one fabricated message.
type Sent struct {
	EventID    string     `json:"event_id"`
	MessageID  string     `json:"message_id"`
	Chat       string     `json:"chat"`
	RootID     string     `json:"root_id,omitempty"`
	ParentID   string     `json:"parent_id,omitempty"`
	Deliveries []Delivery `json:"deliveries"`
}

// Redeliveries is the number of attempts after the first.
func (s Sent) Redeliveries() int { return len(s.Deliveries) - 1 }

// Server is the fake Lark.
type Server struct {
	cfg    Config
	client *http.Client

	mu      sync.Mutex
	rng     *rand.Rand
	seq     int
	cards   []Card
	msgChat map[string]string // message id -> chat, for replies
}

// New returns a Server. Zero Config fields get Lark-like defaults.
func New(cfg Config) *Server {
	if cfg.AckTimeout == 0 {
		cfg.AckTimeout = 3 * time.Second
	}
	if cfg.Redeliver && cfg.MaxRedeliver == 0 {
		cfg.MaxRedeliver = 3
	}
	if cfg.ThinkTime == 0 {
		cfg.ThinkTime = 100 * time.Millisecond
	}
	if cfg.Seed == 0 {
		cfg.Seed = time.Now().UnixNano()
	}
	if cfg.Resolver == nil {
		cfg.Resolver = net.DefaultResolver.LookupHost
	}
	rng := rand.New(rand.NewSource(cfg.Seed)) //nolint:gosec // picks a test endpoint, not a secret
	return &Server{cfg: cfg, client: &http.Client{Timeout: cfg.AckTimeout}, seq: cfg.SeqStart, rng: rng, msgChat: map[string]string{}}
}

// Handler serves the Lark API subset and the driver API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /open-apis/auth/v3/tenant_access_token/internal", s.token)
	mux.HandleFunc("POST /open-apis/im/v1/messages/{id}/reply", s.reply)
	mux.HandleFunc("POST /open-apis/im/v1/messages", s.create)
	mux.HandleFunc("POST /send", s.send)
	mux.HandleFunc("POST /conversation", s.conversation)
	mux.HandleFunc("GET /cards", s.listCards)
	mux.HandleFunc("POST /reset", func(w http.ResponseWriter, _ *http.Request) { s.Reset(); writeJSON(w, 200, map[string]any{"ok": true}) })
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok\n") })
	return mux
}

// Cards returns the recorded cards that pass the filter (empty fields match all).
func (s *Server) Cards(chat, replyTo string, since time.Time) []Card {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Card{}
	for _, c := range s.cards {
		if (chat == "" || c.Chat == chat) && (replyTo == "" || c.ReplyTo == replyTo) && !c.Time.Before(since) {
			out = append(out, c)
		}
	}
	return out
}

// Reset forgets the recorded cards.
func (s *Server) Reset() {
	s.mu.Lock()
	s.cards = nil
	s.mu.Unlock()
}

func (s *Server) nextID(prefix string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return fmt.Sprintf("%s_fake_%d", prefix, s.seq)
}

// --- the Lark API subset ---

func (s *Server) token(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"code": 0, "msg": "ok", "tenant_access_token": "t-fake", "expire": 7200})
}

type postBody struct {
	ReceiveID string `json:"receive_id"`
	MsgType   string `json:"msg_type"`
	Content   string `json:"content"`
}

func (s *Server) reply(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	chat := s.msgChat[id]
	s.mu.Unlock()
	s.record(w, r, chat, id)
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	s.record(w, r, "", "")
}

// record stores a posted card and answers with Lark's success shape.
func (s *Server) record(w http.ResponseWriter, r *http.Request, chat, replyTo string) {
	var b postBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, 400, map[string]any{"code": 99992402, "msg": "invalid body: " + err.Error()})
		return
	}
	if chat == "" {
		chat = b.ReceiveID
	}
	c := Card{Time: time.Now(), MessageID: s.nextID("om"), Chat: chat, ReplyTo: replyTo, Card: json.RawMessage(b.Content)}
	if !json.Valid(c.Card) {
		c.Card = json.RawMessage(strconv.Quote(b.Content))
	}
	c.Text, c.Session, c.Instance = parseCard(b.Content)
	s.mu.Lock()
	s.cards = append(s.cards, c)
	s.msgChat[c.MessageID] = chat
	s.mu.Unlock()
	writeJSON(w, 200, map[string]any{"code": 0, "msg": "success", "data": map[string]any{
		"message_id": c.MessageID, "chat_id": chat, "msg_type": b.MsgType, "create_time": strconv.FormatInt(c.Time.UnixMilli(), 10)}})
}

var footerRe = regexp.MustCompile(`session (\S+) · instance (\S+)`)

// parseCard pulls the body text and the footer's session and instance out of a card.
func parseCard(content string) (text, session, instance string) {
	var c struct {
		Elements []struct {
			Tag      string `json:"tag"`
			Content  string `json:"content"`
			Elements []struct {
				Content string `json:"content"`
			} `json:"elements"`
		} `json:"elements"`
	}
	if json.Unmarshal([]byte(content), &c) != nil {
		return "", "", ""
	}
	for _, e := range c.Elements {
		switch e.Tag {
		case "markdown":
			text = e.Content
		case "note":
			for _, n := range e.Elements {
				if m := footerRe.FindStringSubmatch(n.Content); m != nil {
					session, instance = m[1], m[2]
				}
			}
		}
	}
	return text, session, instance
}

// --- events out ---

type eventSpec struct {
	Chat, Text, RootID, ParentID string
}

func (s *Server) envelope(eventID, messageID string, e eventSpec) []byte {
	content, _ := json.Marshal(map[string]string{"text": e.Text})
	now := strconv.FormatInt(time.Now().UnixMilli(), 10)
	msg := map[string]any{"message_id": messageID, "create_time": now, "chat_id": e.Chat, "chat_type": "group",
		"message_type": "text", "content": string(content)}
	if e.RootID != "" {
		msg["root_id"] = e.RootID
	}
	if e.ParentID != "" {
		msg["parent_id"] = e.ParentID
	}
	b, _ := json.Marshal(map[string]any{
		"schema": "2.0",
		"header": map[string]any{"event_id": eventID, "event_type": "im.message.receive_v1", "create_time": now,
			"token": "", "app_id": "cli_fake", "tenant_key": "fake"},
		"event": map[string]any{
			"sender":  map[string]any{"sender_id": map[string]string{"open_id": "ou_fake_user"}, "sender_type": "user", "tenant_key": "fake"},
			"message": msg,
		},
	})
	return b
}

func (s *Server) endpoints(ctx context.Context) ([]string, error) {
	if len(s.cfg.Bots) > 0 {
		return s.cfg.Bots, nil
	}
	if s.cfg.BotsDNS == "" {
		return nil, fmt.Errorf("no bots: set Bots or BotsDNS")
	}
	host, port, err := net.SplitHostPort(s.cfg.BotsDNS)
	if err != nil {
		return nil, err
	}
	addrs, err := s.cfg.Resolver(ctx, host)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, "http://"+net.JoinHostPort(a, port))
	}
	return out, nil
}

// pick chooses one endpoint at random, like Lark's cluster mode.
func (s *Server) pick(ctx context.Context) (string, error) {
	eps, err := s.endpoints(ctx)
	if err != nil {
		return "", err
	}
	if len(eps) == 0 {
		return "", fmt.Errorf("no bot endpoints resolved")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return eps[s.rng.Intn(len(eps))], nil
}

// Deliver fabricates one message and delivers its event. A delivery that is
// not answered 2xx within AckTimeout is repeated (same event_id), each time to
// a freshly chosen endpoint, up to MaxRedeliver times, when Redeliver is on.
func (s *Server) Deliver(ctx context.Context, chat, text, rootID, parentID string) Sent {
	spec := eventSpec{Chat: chat, Text: text, RootID: rootID, ParentID: parentID}
	messageID, eventID := s.nextID("om"), s.nextID("ev")
	s.mu.Lock()
	s.msgChat[messageID] = chat
	s.mu.Unlock()
	return s.deliverEvent(ctx, Sent{EventID: eventID, MessageID: messageID, Chat: chat, RootID: rootID, ParentID: parentID}, s.envelope(eventID, messageID, spec))
}

func (s *Server) deliverEvent(ctx context.Context, out Sent, body []byte) Sent {
	for attempt := 0; ; attempt++ {
		d := Delivery{}
		ep, err := s.pick(ctx)
		if err != nil {
			d.Error = err.Error()
			out.Deliveries = append(out.Deliveries, d)
			return out
		}
		d.Endpoint = ep
		start := time.Now()
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ep+EventPath, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := s.client.Do(req)
		if err != nil {
			d.Error = err.Error()
		} else {
			d.Status = resp.StatusCode
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		d.Millis = time.Since(start).Milliseconds()
		out.Deliveries = append(out.Deliveries, d)
		if (d.Status >= 200 && d.Status < 300) || !s.cfg.Redeliver || attempt >= s.cfg.MaxRedeliver || ctx.Err() != nil {
			return out
		}
		time.Sleep(s.cfg.RetryDelay)
	}
}

// --- driver API ---

type sendReq struct {
	Chat     string `json:"chat"`
	Text     string `json:"text"`
	ReplyTo  string `json:"reply_to"`
	Count    int    `json:"count"`
	Parallel bool   `json:"parallel"`
}

func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	var q sendReq
	if err := json.NewDecoder(r.Body).Decode(&q); err != nil || q.Chat == "" || q.Text == "" {
		writeJSON(w, 400, map[string]any{"error": "need JSON {chat, text, reply_to?, count?, parallel?}"})
		return
	}
	if q.Count < 1 {
		q.Count = 1
	}
	res := make([]Sent, q.Count)
	one := func(i int) { res[i] = s.Deliver(r.Context(), q.Chat, q.Text, q.ReplyTo, q.ReplyTo) }
	if q.Parallel {
		var wg sync.WaitGroup
		for i := range res {
			wg.Go(func() { ; one(i) })
		}
		wg.Wait()
	} else {
		for i := range res {
			one(i)
		}
	}
	writeJSON(w, 200, map[string]any{"sent": res})
}

type conversationReq struct {
	Chat   string   `json:"chat"`
	Turns  []string `json:"turns"`
	GapMS  int      `json:"gap_ms"`
	WaitMS int      `json:"wait_ms"` // how long to wait for the first card (default 15000)
}

func (s *Server) conversation(w http.ResponseWriter, r *http.Request) {
	var q conversationReq
	if err := json.NewDecoder(r.Body).Decode(&q); err != nil || q.Chat == "" || len(q.Turns) == 0 {
		writeJSON(w, 400, map[string]any{"error": "need JSON {chat, turns:[...], gap_ms?, wait_ms?}"})
		return
	}
	res, err := s.Converse(r.Context(), q.Chat, q.Turns, time.Duration(q.GapMS)*time.Millisecond, time.Duration(q.WaitMS)*time.Millisecond)
	out := map[string]any{"sent": res}
	if err != nil {
		out["error"] = err.Error()
		writeJSON(w, 504, out)
		return
	}
	writeJSON(w, 200, out)
}

// Converse sends turns[0], waits for the bot's card (a reply to it), then
// sends the other turns as replies inside that thread: root_id is the first
// message, parent_id the bot's card, one every gap.
func (s *Server) Converse(ctx context.Context, chat string, turns []string, gap, wait time.Duration) ([]Sent, error) {
	if wait <= 0 {
		wait = 15 * time.Second
	}
	first := s.Deliver(ctx, chat, turns[0], "", "")
	res := []Sent{first}
	if len(turns) == 1 {
		return res, nil
	}
	var card Card
	deadline := time.Now().Add(wait)
	for {
		if cs := s.Cards(chat, first.MessageID, time.Time{}); len(cs) > 0 {
			card = cs[0]
			break
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return res, fmt.Errorf("no card in reply to %s within %s", first.MessageID, wait)
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(s.cfg.ThinkTime) // the bot posts the card just before its Session is free again; a person is slower than that
	rest := make([]Sent, len(turns)-1)
	var wg sync.WaitGroup
	for i, t := range turns[1:] {
		if i > 0 {
			time.Sleep(gap)
		}
		wg.Go(func() { ; rest[i] = s.Deliver(ctx, chat, t, first.MessageID, card.MessageID) })
	}
	wg.Wait()
	return append(res, rest...), nil
}

func (s *Server) listCards(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var since time.Time
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			writeJSON(w, 400, map[string]any{"error": "since must be RFC 3339"})
			return
		}
		since = t
	}
	writeJSON(w, 200, map[string]any{"cards": s.Cards(q.Get("chat"), q.Get("reply_to"), since)})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
