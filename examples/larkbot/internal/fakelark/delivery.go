package fakelark

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

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
