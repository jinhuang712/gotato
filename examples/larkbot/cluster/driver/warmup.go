package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// The warm-up's budget: how long it tries, how long it waits for one card, how often it looks, and how long it
// pauses after a message that could not be sent.
const (
	warmUpLimit  = 90 * time.Second
	warmCardWait = 8 * time.Second
	warmCardPoll = 300 * time.Millisecond
	warmUpRetry  = 2 * time.Second
)

// warmUp sends a throwaway message through the whole path (fakelark, a bot, the fake model endpoint, the card back)
// until a card arrives. Right after `up` a Service's rules may not be programmed yet although the rollout is
// ready, so the first messages can fail inside the cluster. It then clears fakelark's cards. The throwaway
// Session is in no thread, so no check sees it.
func (e *env) warmUp(ctx context.Context) bool {
	start := time.Now()
	id := runID()
	for attempt := 1; time.Since(start) < warmUpLimit && ctx.Err() == nil; attempt++ {
		t := &thread{chat: fmt.Sprintf("oc_warm_%s_%02d", id, attempt)}
		if m := e.say(ctx, t, fmt.Sprintf("warm-%d", attempt), ""); m.err != nil {
			sleep(ctx, warmUpRetry)
		} else if e.cardBack(ctx, t) {
			fmt.Printf("warm-up: a card came back on attempt %d after %s\n", attempt, round(time.Since(start)))
			return true
		}
	}
	fmt.Printf("warm-up: no card within %s\n", time.Since(start).Round(time.Second))
	e.printErrors()
	return false
}

// cardBack waits for the bot's card in the thread and for its Record to be readable, then clears fakelark's cards.
func (e *env) cardBack(ctx context.Context, t *thread) bool {
	for deadline := time.Now().Add(warmCardWait); time.Now().Before(deadline); sleep(ctx, warmCardPoll) {
		if cs, _ := e.cards(ctx, t.chat); len(cs) > 0 {
			if _, err := e.record(ctx, t.session()); err != nil { // the read path, through the larkbot Service, too
				continue
			}
			_ = e.do(ctx, "POST", e.larkURL()+"/reset", nil, nil)
			return true
		}
	}
	return false
}

var scrubRe = regexp.MustCompile(`(?i)(secret|token|authorization|api_key|apikey)=("[^"]*"|\S+)|Bearer \S+|t-[A-Za-z0-9]{6,}`)

// printErrors prints the bots' ERROR and WARN lines, secrets scrubbed, so a failure explains itself.
func (e *env) printErrors() {
	fmt.Println("bot ERROR/WARN lines (last 15):")
	lines := e.errorLines()
	if len(lines) > errorLinesShown {
		lines = lines[len(lines)-errorLinesShown:]
	}
	for _, l := range lines {
		fmt.Println("  " + l)
	}
	if len(lines) == 0 {
		fmt.Println("  (none)")
	}
}

// errorLinesShown is how many of the bots' ERROR and WARN lines printErrors shows (its heading says so).
const errorLinesShown = 15

// errorLines is the bots' ERROR and WARN lines, one per log line, secrets scrubbed.
func (e *env) errorLines() []string {
	var lines []string
	for _, l := range e.readLogs() {
		if lv := l.KV["level"]; lv == "ERROR" || lv == "WARN" {
			var kv []string
			for _, k := range []string{"time", "level", "msg", "instance", "session", "message_id", "err"} {
				if v, ok := l.KV[k]; ok {
					kv = append(kv, k+"="+v)
				}
			}
			lines = append(lines, l.Pod+" "+scrubRe.ReplaceAllString(strings.Join(kv, " "), "$1=***"))
		}
	}
	return lines
}
