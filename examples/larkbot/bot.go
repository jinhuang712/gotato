package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/httpapi"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// Session metadata the bot keeps, so any instance can find the chat.
const (
	metaChatID = "lark.chat_id"
	metaThread = "lark.thread" // the thread key: a message_id, the root of the reply chain
)

// target is where a Session's reply goes.
type target struct{ chatID, messageID string }

// busyNotice is what the user is told, once, when a message could not be handed to the Run that is going.
const busyNotice = "I am busy right now, please try again shortly."

// sender posts Lark messages. content is the `content` JSON of an interactive card.
type sender interface {
	Reply(ctx context.Context, messageID, content string) error
	Create(ctx context.Context, chatID, content string) error
}

// larkSender sends through the Lark API.
type larkSender struct{ api *lark.Client }

func (l larkSender) Reply(ctx context.Context, messageID, content string) error {
	resp, err := l.api.Im.Message.Reply(ctx, larkim.NewReplyMessageReqBuilder().MessageId(messageID).
		Body(larkim.NewReplyMessageReqBodyBuilder().MsgType("interactive").Content(content).Build()).Build())
	if err == nil && !resp.Success() {
		err = errors.New(resp.Msg)
	}
	return err
}

func (l larkSender) Create(ctx context.Context, chatID, content string) error {
	resp, err := l.api.Im.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().ReceiveIdType("chat_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().ReceiveId(chatID).MsgType("interactive").Content(content).Build()).Build())
	if err == nil && !resp.Success() {
		err = errors.New(resp.Msg)
	}
	return err
}

// bot connects Lark messages to a Gotato Runner.
type bot struct {
	runner *service.Runner
	send   sender
	agent  string
	model  string // shown in the card footer
	label  string // instance label shown in the card and the logs
	log    *slog.Logger
	client *http.Client // forwards a message to the writer's address

	mu      sync.Mutex
	targets map[string]target // Session ID -> latest chat and user message (in memory)
	replies map[gotato.RunID]string
	seen    *seenSet
	gates   map[string]*gate // Session ID -> the messages waiting behind one that retries
	retry   retryPolicy

	ackDelay time.Duration // test only: hold the event's HTTP answer this long, so the sender sees a late ack
}

func newBot(runner *service.Runner, send sender, agent, model, label string, log *slog.Logger) *bot {
	return &bot{runner: runner, send: send, agent: agent, model: model, label: label, log: log,
		client: &http.Client{Timeout: 10 * time.Minute, // a forward can start a Run and wait for it
			Transport: &http.Transport{DialContext: (&net.Dialer{Timeout: 3 * time.Second}).DialContext, MaxIdleConnsPerHost: 4}},
		targets: map[string]target{}, replies: map[gotato.RunID]string{}, seen: newSeenSet(1024),
		gates: map[string]*gate{}, retry: defaultRetry(defaultRetryBudget)}
}

func (b *bot) card(session string) cardInfo {
	return cardInfo{Session: session, Instance: b.label, Model: b.model}
}

// short cuts text for a one-line log.
func short(s string) string {
	if r := []rune(s); len(r) > 40 {
		return string(r[:40]) + "..."
	}
	return s
}

// onMessage handles one im.message.receive_v1 event. It returns at once; the
// work runs in a goroutine, because Lark wants an answer within 3 seconds.
func (b *bot) onMessage(ctx context.Context, ev *larkim.P2MessageReceiveV1) error {
	if ev == nil || ev.Event == nil || ev.Event.Message == nil {
		return nil
	}
	m := ev.Event.Message
	if ev.Event.Sender != nil && deref(ev.Event.Sender.SenderType) != "user" {
		return nil // the bot's own messages, and other apps'
	}
	if b.ackDelay > 0 {
		// Late ack: the work below still starts at once; only the answer to Lark is held back.
		defer time.Sleep(b.ackDelay)
	}
	messageID, chatID := deref(m.MessageId), deref(m.ChatId)
	thread := threadKeyOf(messageID, deref(m.RootId), deref(m.ParentId))
	text, ok := textOf(deref(m.MessageType), deref(m.Content))
	if !ok {
		b.log.Info("ignored message", "type", deref(m.MessageType), "message_id", messageID)
		return nil
	}
	b.mu.Lock()
	fresh := b.seen.add(messageID)
	b.mu.Unlock()
	if !fresh {
		b.log.Warn("duplicate suspected", "chat", chatID, "instance", b.label, "message_id", messageID)
		return nil
	}
	if chatID == "" {
		return nil
	}
	b.log.Info("message received", "chat", chatID, "instance", b.label, "message_id", messageID,
		"thread", thread, "session", sessionIDFor(thread), "text", short(text))
	go b.handle(context.WithoutCancel(ctx), chatID, thread, messageID, text)
	return nil
}

// handle makes sure the thread's Session exists, then sends the text to it.
func (b *bot) handle(ctx context.Context, chatID, thread, messageID, text string) {
	id := sessionIDFor(thread)
	b.mu.Lock()
	b.targets[id] = target{chatID: chatID, messageID: messageID}
	b.mu.Unlock()

	_, err := b.runner.CreateSessionExclusive(ctx, b.agent, id, map[string]string{metaChatID: chatID, metaThread: thread})
	if err != nil && !errors.Is(err, service.ErrSessionExists) {
		b.log.Error("create session", "session", id, "message_id", messageID, "err", err)
		return
	}
	b.deliver(ctx, chatID, id, messageID, text)
}

// deliver sends the text to the Session, and sends it again while the failure
// is a definite "not run" one (see retry.go). Each attempt calls Runner.Send
// anew, which finds out who the writer is: once the old writer's Liveness row
// expires, this instance takes the Session over and starts the Run. When the
// budget is spent, the user is told once. The reply comes from the Event
// stream either way (replyLoop).
func (b *bot) deliver(ctx context.Context, chatID, id, messageID, text string) {
	start := time.Now()
	// Messages of one Session keep their order: one that comes while an earlier
	// message retries waits for it.
	wait, leave, gated := b.enter(id, false)
	defer func() { leave() }()
	if !b.await(ctx, wait) {
		return
	}
	backoff := b.retry.first
	for attempt := 1; ; attempt++ {
		err := b.attempt(ctx, chatID, id, messageID, text, func() { leave() })
		if err == nil {
			if attempt > 1 {
				b.log.Info("delivered after retry", "session", id, "message_id", messageID, "attempts", attempt,
					"elapsed", time.Since(start).Round(time.Millisecond), "instance", b.label)
			}
			return
		}
		if !notRun(err) {
			if _, ok := errors.AsType[*deliveryError](err); ok {
				b.log.Warn("delivery unknown", "chat", chatID, "instance", b.label, "session", id, "message_id", messageID, "attempts", attempt, "err", err)
				b.notify(ctx, id, chatID, messageID, busyNotice)
			} else {
				b.log.Error("send", "session", id, "message_id", messageID, "err", err)
			}
			return
		}
		if b.retry.budget <= 0 || time.Since(start)+backoff > b.retry.budget || ctx.Err() != nil {
			b.log.Warn("delivery failed, not run", "chat", chatID, "instance", b.label, "session", id, "message_id", messageID,
				"attempts", attempt, "elapsed", time.Since(start).Round(time.Millisecond), "err", err)
			b.notify(ctx, id, chatID, messageID, busyNotice)
			return
		}
		if !gated {
			// From here on, a later message of this Session waits for this one.
			var w <-chan struct{}
			w, leave, gated = b.enter(id, true)
			if !b.await(ctx, w) {
				return
			}
		}
		b.log.Info("not run, will retry", "instance", b.label, "session", id, "message_id", messageID, "attempt", attempt, "in", backoff, "err", err)
		if !sleep(ctx, backoff) {
			return
		}
		if backoff *= 2; backoff > b.retry.max {
			backoff = b.retry.max
		}
	}
}

// await waits for the earlier message of the Session (nil: none). False when ctx ends first.
func (b *bot) await(ctx context.Context, wait <-chan struct{}) bool {
	if wait == nil {
		return true
	}
	select {
	case <-wait:
		return true
	case <-ctx.Done():
		return false
	}
}

// attempt is one try. It returns nil when the message was accepted, and
// otherwise an error: a *deliveryError for a forward, with notRun set when
// nothing was delivered, or Send's own error. accepted is called when a Send
// has gone on longer than acceptGrace, since a refusal comes back at once.
func (b *bot) attempt(ctx context.Context, chatID, id, messageID, text string, accepted func()) error {
	// Send queues the text when a Run is going here, and otherwise runs it and
	// returns when that Run settles.
	tm := time.AfterFunc(acceptGrace, accepted)
	res, err := b.runner.Send(ctx, service.MessageRequest{SessionID: id, Kind: service.MessageKindFollowUp, Text: text})
	tm.Stop()
	switch {
	case err == nil && res.Queued:
		b.log.Info("send queued", "chat", chatID, "instance", b.label, "session", id, "message_id", messageID)
		return nil
	case err == nil:
		b.log.Info("send ran a new run", "chat", chatID, "instance", b.label, "session", id, "message_id", messageID)
		return nil
	}
	addr, ok := service.WriterOf(err)
	if !ok {
		return err
	}
	b.log.Info("send not_writer, forwarding", "chat", chatID, "instance", b.label, "session", id, "message_id", messageID, "to", addr)
	if ferr := b.forward(ctx, addr, id, text); ferr != nil {
		b.log.Warn("forward failed", "chat", chatID, "instance", b.label, "session", id, "message_id", messageID, "to", addr, "err", ferr)
		return ferr
	}
	b.log.Info("forwarded", "chat", chatID, "instance", b.label, "session", id, "message_id", messageID, "to", addr)
	return nil
}

// forward sends the message to the writer's endpoint with the bot's own
// client. The Forwarded header tells the writer's handler never to forward it
// again. Any answer other than 200 or 202 (including a second not_writer) is an error.
func (b *bot) forward(ctx context.Context, addr, sessionID, text string) error {
	if addr == "" {
		return &deliveryError{err: errors.New("the writer advertises no address"), notRun: true}
	}
	body, _ := json.Marshal(map[string]string{"kind": string(service.MessageKindFollowUp), "text": text})
	target := addr + "/v1/sessions/" + url.PathEscape(sessionID) + "/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(httpapi.ForwardedHeader, "true")
	resp, err := b.client.Do(req)
	if err != nil {
		// A dial failure delivered nothing; a failure after the connection was made may have.
		return &deliveryError{err: err, notRun: notConnected(err)}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusAccepted {
		return nil
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return &deliveryError{err: fmt.Errorf("writer answered %d: %s", resp.StatusCode, bytes.TrimSpace(msg)),
		notRun: resp.Header.Get(httpapi.NotRunHeader) == "true"}
}

// notify tells the user something as a card with the footer: as a reply to
// their message when there is one, and as a new message to the chat otherwise.
func (b *bot) notify(ctx context.Context, sessionID, chatID, messageID, text string) {
	ctx = context.WithoutCancel(ctx)
	content := cardContent(b.card(sessionID), text)
	var err error
	if messageID != "" {
		err = b.send.Reply(ctx, messageID, content)
	} else {
		err = b.send.Create(ctx, chatID, content)
	}
	if err != nil {
		b.log.Error("post notice", "session", sessionID, "err", err)
	}
}

// replyLoop reads the Instance's Event stream and, when a Run ends, posts its
// final reply to the chat. It returns when ctx ends or the stream does.
func (b *bot) replyLoop(ctx context.Context) {
	sub, err := b.runner.SubscribeInstance(ctx, service.ForAgents(b.agent))
	if err != nil {
		b.log.Error("subscribe", "err", err)
		return
	}
	defer func() { _ = sub.Close() }()
	for {
		ev, err := sub.Next(ctx)
		if err != nil {
			if !errors.Is(err, io.EOF) && ctx.Err() == nil {
				b.log.Error("event stream", "err", err)
			}
			return
		}
		switch e := ev.(type) {
		case gotato.ReplyEnded:
			// The last reply of a Run is its final text; earlier ones end in tool calls.
			if e.ToolCalls == 0 && isBotSession(string(e.SessionID)) {
				b.mu.Lock()
				b.replies[e.RunID] = gotato.TextOf(e.Message)
				b.mu.Unlock()
			}
		case gotato.RunEnded:
			if !isBotSession(string(e.SessionID)) {
				continue
			}
			b.mu.Lock()
			text := b.replies[e.RunID]
			delete(b.replies, e.RunID)
			b.mu.Unlock()
			b.post(ctx, string(e.SessionID), replyCard(b.card(string(e.SessionID)), e.Status, text))
		}
	}
}

// post sends content for a Session: as a reply to the user's last message when
// this process saw it, else as a reply to the thread's root message, else as a
// new message to the chat (both read from the Session's metadata).
func (b *bot) post(ctx context.Context, sessionID, content string) {
	b.mu.Lock()
	t, ok := b.targets[sessionID]
	b.mu.Unlock()
	if !ok {
		rec, err := b.runner.Record(ctx, sessionID)
		if err != nil {
			b.log.Error("read session for chat", "session", sessionID, "err", err)
			return
		}
		chat, _ := rec.Get(metaChatID)
		if chat == "" {
			b.log.Warn("session has no chat_id", "session", sessionID)
			return
		}
		// lark.thread is a message_id: reply to the thread's root message.
		thread, _ := rec.Get(metaThread)
		t = target{chatID: chat, messageID: thread}
	}
	// Replies and sends outlive the shutdown signal briefly; use their own ctx.
	ctx = context.WithoutCancel(ctx)
	var err error
	if t.messageID != "" {
		err = b.send.Reply(ctx, t.messageID, content)
	} else {
		err = b.send.Create(ctx, t.chatID, content)
	}
	if err != nil {
		b.log.Error("post reply", "session", sessionID, "err", err)
		return
	}
	b.log.Info("reply posted", "chat", t.chatID, "instance", b.label, "session", sessionID)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
