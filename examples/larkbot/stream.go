package main

import (
	"context"
	"strings"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
)

// A streaming card: for a Run this process starts, the bot posts a card at
// once, updates it while the model writes, and ends it as the final text.
//
//   - streamRun starts the Run with Runner.StreamRun, so its sink gets the
//     text_delta Events. Runner.Send has no sink.
//   - The card is posted first, so its own id can go into the Session's
//     metadata (metaCard) with the Run's first write. A pod that takes the
//     Session over can read it there. This example does not: it keeps the card
//     in memory, and a Run another pod finishes is answered with a new card.
//   - A message sent into a Run that is going is queued by Send. It has no
//     deltas and no card of its own; the Run's card ends as the Run's final reply.
//   - The card is finished from the Run's response, not from the Events, so a
//     skipped delta or a dropped Event cannot leave it half written.

const defaultStreamEvery = 500 * time.Millisecond

// thinking is the text of a card before the first update.
const thinking = "..."

// liveCard is the card of one streamed Run.
type liveCard struct {
	id    string
	runID gotato.RunID // set at run_started; guarded by bot.mu
}

// streamRun starts the Run for text on the Session and streams it into a new
// card, which replies to the user's message. ran is false when no Run started
// here (the Session is busy, or another pod writes it, or the card could not be
// posted); the caller then uses Send, which queues, forwards or retries.
//
// StreamRun is not Send: it neither queues into a Run that is going nor answers
// not_writer (it waits for a busy Session instead). So the Record is read first,
// and only an idle Session is streamed. Another message can still start a Run
// between that read and StreamRun; StreamRun is then refused, and the card is recalled.
func (b *bot) streamRun(ctx context.Context, sessionID, messageID, text string) (ran bool, err error) {
	if messageID == "" || !b.idle(ctx, sessionID) {
		return false, nil
	}
	info := b.card(sessionID)
	cardID, perr := b.send.Reply(ctx, messageID, cardContent(info, thinking))
	if perr != nil || cardID == "" {
		b.log.Error("post card", "session", sessionID, "message_id", messageID, "err", perr)
		return false, nil
	}
	lc := &liveCard{id: cardID}
	update := func(content string) {
		if err := b.send.Update(ctx, cardID, content); err != nil {
			b.log.Warn("update card", "session", sessionID, "card", cardID, "err", err)
		}
	}

	// The sink runs on this goroutine, so draft and sentAt need no lock. One
	// sink serves one Run, which is the buffer by RunID.
	var draft strings.Builder
	sentAt := time.Now()
	sink := func(e gotato.Event) error {
		switch e := e.(type) {
		case gotato.RunStarted:
			b.mu.Lock()
			lc.runID = e.RunID
			b.mu.Unlock()
		case gotato.ReplyStarted:
			draft.Reset() // the last reply is the answer; a reply before a tool call was a draft
		case gotato.TextDelta:
			draft.WriteString(e.Text)
			if time.Since(sentAt) >= b.streamEvery {
				sentAt = time.Now()
				update(cardContent(info, draft.String()))
			}
		}
		return nil // an error would stop delivery, not the Run
	}

	req := service.RunRequest{SessionID: sessionID, Prompt: text, RequestID: messageID, Update: map[string]string{metaCard: cardID}}
	resp, err := b.runner.StreamRunAdmitted(ctx, req, func() { b.setCard(sessionID, lc) }, sink)
	b.mu.Lock()
	if lc.runID == "" {
		lc.runID = resp.Result.RunID
	}
	b.mu.Unlock()
	if resp.SessionID == "" {
		// Refused before it ran: the card was never used. Recall it, so a
		// forwarded or queued message leaves no empty card behind.
		if derr := b.send.Delete(ctx, cardID); derr != nil {
			b.log.Warn("recall card", "session", sessionID, "card", cardID, "err", derr)
		}
		return false, nil
	}
	update(replyCard(info, resp.Result.Status, resp.FinalText))
	return true, err
}

func (b *bot) setCard(sessionID string, lc *liveCard) {
	b.mu.Lock()
	b.cards[sessionID] = lc
	b.mu.Unlock()
}

// idle says whether no Instance holds the Session: no Run is going on it, here or elsewhere.
func (b *bot) idle(ctx context.Context, sessionID string) bool {
	rec, err := b.runner.Record(ctx, sessionID)
	if err != nil {
		return false // Send finds out what is wrong
	}
	_, busy := rec.Holder()
	return !busy
}

// ownsRun says whether runID is a Run streamRun answers in its own card, so the
// reply loop must not post it again. A card whose Run id is not known yet owns
// the Run that is ending: it was admitted, so no other Run can be going on the Session.
func (b *bot) ownsRun(sessionID string, runID gotato.RunID) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	lc, ok := b.cards[sessionID]
	if !ok || (lc.runID != "" && lc.runID != runID) {
		return false
	}
	if lc.runID != "" {
		delete(b.cards, sessionID) // its Run has ended and been seen
	}
	return true
}
