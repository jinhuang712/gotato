package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// s2PrintTakeover prints the first claim after the kill by another instance, and the events after it.
func s2PrintTakeover(r record, victimInst string, killAt time.Time) {
	var took time.Duration
	after := false
	var tail []string
	for _, ev := range r.Events {
		if ev.Kind == "session_claimed" && ev.Instance != victimInst && ev.From == victimInst && !after {
			after = true
			took = ev.Time.Sub(killAt)
			fmt.Printf("    takeover: claimed by %.8s (why=%s epoch=%d) %s after the kill\n", ev.Instance, ev.Why, ev.Epoch, round(took))
		}
		if after && ev.Kind != "reply_part_kept" && ev.Kind != "context_built" {
			tail = append(tail, ev.Kind+eventNote(ev))
		}
	}
	if !after {
		fmt.Println("    no takeover claim in the Record")
	}
	fmt.Printf("    events after the takeover: %s\n", strings.Join(tail, ", "))
}

// The characters of a card's text and of a message the driver did not send that s2PrintCards prints.
const (
	cardTextChars    = 50
	foreignTextChars = 120
)

// s2PrintCards prints the thread's cards and the messages in its Record the driver did not send.
func (e *env) s2PrintCards(ctx context.Context, t *thread, r record, start time.Time) {
	cs, _ := e.cards(ctx, t.chat)
	for _, c := range cs {
		fmt.Printf("    card +%02ds from %s: %q\n", int(c.Time.Sub(start).Seconds()), c.Instance, trunc(c.Text, cardTextChars))
	}
	for i := range r.Messages {
		if r.Messages[i].Role != "assistant" && !strings.Contains(r.text(i), "T[") {
			fmt.Printf("    non-driver message (%s): %q\n", r.Messages[i].Role, trunc(r.text(i), foreignTextChars))
		}
	}
}
