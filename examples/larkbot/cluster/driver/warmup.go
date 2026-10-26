package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// warmUp sends a throwaway message through the whole path (fakelark, a bot, the fake gateway, the card back)
// until a card arrives. Right after `up` a Service's rules may not be programmed yet although the rollout is
// ready, so the first messages can fail inside the cluster. It then clears fakelark's cards. The throwaway
// Session is in no thread, so no check sees it.
func (e *env) warmUp(ctx context.Context) bool {
	start := time.Now()
	id := runID()
	for attempt := 1; time.Since(start) < 90*time.Second && ctx.Err() == nil; attempt++ {
		t := &thread{chat: fmt.Sprintf("oc_warm_%s_%02d", id, attempt)}
		if m := e.say(ctx, t, fmt.Sprintf("warm-%d", attempt), ""); m.err == nil {
			for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); sleep(ctx, 300*time.Millisecond) {
				if cs, _ := e.cards(ctx, t.chat); len(cs) > 0 {
					if _, err := e.record(ctx, t.session()); err != nil { // the read path, through the larkbot Service, too
						continue
					}
					_ = e.do(ctx, "POST", e.larkURL()+"/reset", nil, nil)
					fmt.Printf("warm-up: a card came back on attempt %d after %s\n", attempt, time.Since(start).Round(100*time.Millisecond))
					return true
				}
			}
		} else {
			sleep(ctx, 2*time.Second)
		}
	}
	fmt.Printf("warm-up: no card within %s\n", time.Since(start).Round(time.Second))
	e.printErrors()
	return false
}

var scrubRe = regexp.MustCompile(`(?i)(secret|token|authorization|api_key|apikey)=("[^"]*"|\S+)|Bearer \S+|t-[A-Za-z0-9]{6,}`)

// printErrors prints the bots' ERROR and WARN lines, secrets scrubbed, so a failure explains itself.
func (e *env) printErrors() {
	n := 0
	fmt.Println("bot ERROR/WARN lines (last 15):")
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
	if len(lines) > 15 {
		lines = lines[len(lines)-15:]
	}
	for _, l := range lines {
		fmt.Println("  " + l)
		n++
	}
	if n == 0 {
		fmt.Println("  (none)")
	}
}
