package main

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// msg is one user message the driver sent, and what happened to it.
type msg struct {
	tag     string // unique text marker, so the message is found in the Transcript
	replyTo string
	sent    sent
	err     error
	at      time.Time
}

// thread is one Lark thread: one chat, one Session.
type thread struct {
	chat string
	n    int    // 1-based number in its scenario, for the tags
	root string // the first message's ID; the Session is lark-<root>
	msgs []*msg

	mu sync.Mutex // say may run in parallel for one thread (s4, s5); the first message is sent alone
}

func (t *thread) session() string { return sessionOf(t.root) }

const (
	gapKnown = "KNOWN-BOT-GAP"
	fault    = "GOTATO-FAULT"
	unexpl   = "UNEXPLAINED"
)

// say sends one message into a thread (the first one starts it).
func (e *env) say(ctx context.Context, t *thread, tag, directive string) *msg {
	m := &msg{tag: "T[" + tag + "]", at: time.Now()}
	t.mu.Lock()
	m.replyTo = t.root
	t.mu.Unlock()
	text := m.tag
	if directive != "" {
		text += " " + directive
	}
	m.sent, m.err = e.send(ctx, t.chat, text, m.replyTo)
	t.mu.Lock()
	if t.root == "" && m.err == nil {
		t.root = m.sent.MessageID
	}
	t.msgs = append(t.msgs, m)
	t.mu.Unlock()
	return m
}

func inFlight(r record) int {
	started, ended := 0, 0
	for _, ev := range r.Events {
		switch ev.Kind {
		case "run_started":
			started++
		case "run_ended":
			ended++
		}
	}
	return started - ended
}

// waitQuiet returns when no thread has a Run in flight and the cards have not changed for quiet, or at max.
func (e *env) waitQuiet(ctx context.Context, ts []*thread, quiet, limit time.Duration) {
	deadline := time.Now().Add(limit)
	last, lastChange := -1, time.Now()
	for time.Now().Before(deadline) && ctx.Err() == nil {
		total, busy := 0, 0
		for _, t := range ts {
			if cs, err := e.cards(ctx, t.chat); err == nil {
				total += len(cs)
			}
			if t.root != "" {
				if r, err := e.record(ctx, t.session()); err == nil {
					busy += inFlight(r)
				}
			}
		}
		if total != last {
			last, lastChange = total, time.Now()
		}
		if busy == 0 && time.Since(lastChange) >= quiet {
			return
		}
		sleep(ctx, 2*time.Second)
	}
}

// outcome of a message that is not in its Transcript exactly once, from the bot logs.
func (e *env) classify(m *msg, count int, logs []logLine) (string, string) {
	if count == 1 {
		return "ok", ""
	}
	var mine []logLine
	pods := map[string]bool{}
	for _, l := range logs {
		if m.sent.MessageID != "" && l.KV["message_id"] == m.sent.MessageID {
			mine = append(mine, l)
			if l.Msg == "message received" {
				pods[l.Pod] = true
			}
		}
	}
	if count > 1 {
		dup := false
		for _, l := range mine {
			dup = dup || l.Msg == "duplicate suspected"
		}
		if len(m.sent.Deliveries) > 1 || dup {
			return gapKnown, fmt.Sprintf("duplicate from redelivery (%d deliveries, on %d pods)", len(m.sent.Deliveries), len(pods))
		}
		return unexpl, fmt.Sprintf("in the Transcript %d times, delivered once", count)
	}
	// count == 0
	if m.err != nil || !m.sent.acked() {
		return gapKnown, "never acked by a bot (no 2xx in any delivery)"
	}
	has := func(msg string) bool {
		for _, l := range mine {
			if l.Msg == msg {
				return true
			}
		}
		return false
	}
	switch {
	case e.acceptedByVictim(mine):
		return gapKnown, "accepted into the memory of the writer that was then killed (queued, not committed)"
	case has("send queued"), has("send ran a new run"), has("forwarded"):
		return fault, "the bot's Send said queued/ran/forwarded, the message is not in the Transcript"
	case has("forward failed"):
		return gapKnown, "forward to the writer failed (user got the busy notice)"
	case has("send"):
		for _, l := range mine {
			if l.Msg == "send" {
				err := l.KV["err"]
				switch {
				case strings.Contains(err, "drain"):
					return gapKnown, "refused: draining"
				case strings.Contains(err, "busy"):
					return gapKnown, "refused: busy"
				case strings.Contains(err, "limit_exceeded"):
					return gapKnown, "refused: limit_exceeded"
				}
				return gapKnown, "send error: " + err
			}
		}
	case has("create session"):
		return gapKnown, "create session failed"
	case has("send not_writer, forwarding"):
		return gapKnown, "forwarding, no outcome logged (pod gone)"
	case has("message received"):
		return gapKnown, "received, no outcome logged (pod gone before Send returned)"
	}
	return unexpl, "acked, but no pod logged it"
}

// acceptedByVictim says the message was queued by, or forwarded to, the pod that s2 killed.
func (e *env) acceptedByVictim(lines []logLine) bool {
	if e.victimPod == "" {
		return false
	}
	for _, l := range lines {
		if l.Msg == "send queued" && l.Pod == e.victimPod {
			return true
		}
		if l.Msg == "forwarded" && strings.Contains(l.KV["to"], "//"+e.victimIP+":") {
			return true
		}
	}
	return false
}

// report checks every thread against its Record, the cards and the logs, prints one line per problem and the
// totals, and returns the number of GOTATO-FAULTs.
func (e *env) report(ctx context.Context, ts []*thread) int {
	logs := e.readLogs()
	cat := map[string]int{}
	faults := 0
	var sentN, ackedN, onceN, missingN, dupN, cardN, noticeN, runN, dupCards, lostCards int
	byReason := map[string]int{}
	for _, t := range ts {
		if t.root == "" {
			continue
		}
		rec, err := e.record(ctx, t.session())
		if err != nil { // no Session: the first message never got in; each message is classified from the logs
			rec = record{}
		}
		for _, f := range recordFaults(rec) {
			faults++
			fmt.Printf("  %s %s: %s\n", fault, t.session(), f)
		}
		for _, m := range t.msgs {
			sentN++
			if m.err == nil && m.sent.acked() {
				ackedN++
			}
			n := rec.count(m.tag)
			c, why := e.classify(m, n, logs)
			switch n {
			case 1:
				onceN++
			case 0:
				missingN++
			default:
				dupN++
			}
			if c == "ok" {
				continue
			}
			cat[c]++
			byReason[c+": "+why]++
			if c == fault {
				faults++
			}
			if c == fault || c == unexpl {
				fmt.Printf("  %s %s %s: %s\n", c, t.session(), m.tag, why)
			}
		}
		cs, _ := e.cards(ctx, t.chat)
		shown := 0
		for _, c := range cs {
			if strings.Contains(c.Text, busyNoticePrefix) {
				noticeN++
			} else {
				shown++
			}
		}
		cardN += shown
		runN += len(rec.Runs)
		if shown > len(rec.Runs) {
			dupCards += shown - len(rec.Runs)
		} else {
			lostCards += len(rec.Runs) - shown
		}
	}
	fmt.Printf("\n  messages sent %d, acked by a bot %d, in a Transcript once %d, missing %d, more than once %d\n", sentN, ackedN, onceN, missingN, dupN)
	fmt.Printf("  Runs %d, reply cards %d (extra %d, missing %d), busy notices %d\n", runN, cardN, dupCards, lostCards, noticeN)
	var keys []string
	for k := range byReason {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("  %-60s x%d\n", k, byReason[k])
	}
	fmt.Printf("  GOTATO-FAULT %d, KNOWN-BOT-GAP %d, UNEXPLAINED %d\n", faults, cat[gapKnown], cat[unexpl])
	return faults
}

var storeErrRe = regexp.MustCompile(`(?i)epoch|fenc|corrupt|checksum`)

// storeErrors returns the bots' WARN and ERROR lines that point at the store: an epoch or
// fencing complaint, a corrupt file. Each is a GOTATO-FAULT until read.
func storeErrors(logs []logLine) []string {
	var out []string
	for _, l := range logs {
		if lv := l.KV["level"]; lv != "ERROR" && lv != "WARN" {
			continue
		}
		if storeErrRe.MatchString(l.KV["err"]) || storeErrRe.MatchString(l.Msg) {
			out = append(out, fmt.Sprintf("%s %s session=%s err=%s", l.Pod, l.Msg, l.KV["session"], l.KV["err"]))
		}
	}
	return out
}

// dupStats is what the Records and the cards say about messages delivered more than once (s5). Every message
// is meant to be the only one of its kind in its Run, so the expected cards and replies of a thread are its
// messages.
type dupStats struct {
	Messages           int
	Attempts           map[int]int // deliveries of a message -> how many messages
	DeliveredTwice     int         // messages with more than one delivery
	ExtraDeliveries    int
	Acked              int // messages with a 2xx in some delivery
	InTranscriptTwice  int // messages found more than once in their Transcript
	ExtraUserCopies    int
	MaxCopies          int
	ExtraAssistant     int // assistant messages beyond one per message
	ExtraCards         int // cards beyond one per message
	MissingCards       int
	DupSuspectedCaught int         // redeliveries the receiving pod's seen-set caught
	PodsHandled        map[int]int // distinct pods that handled a message -> how many messages
}

// addThread counts one thread: its sent messages, the thread's Record and cards (busy notices left out).
func (d *dupStats) addThread(msgs []*msg, rec record, cards int, logs []logLine) {
	if d.Attempts == nil {
		d.Attempts, d.PodsHandled = map[int]int{}, map[int]int{}
	}
	for _, m := range msgs {
		d.Messages++
		n := len(m.sent.Deliveries)
		d.Attempts[n]++
		if n > 1 {
			d.DeliveredTwice++
			d.ExtraDeliveries += n - 1
		}
		if m.sent.acked() {
			d.Acked++
		}
		if c := rec.count(m.tag); c > 1 {
			d.InTranscriptTwice++
			d.ExtraUserCopies += c - 1
			d.MaxCopies = max(d.MaxCopies, c)
		}
		pods := map[string]bool{}
		for _, l := range logs {
			if m.sent.MessageID == "" || l.KV["message_id"] != m.sent.MessageID {
				continue
			}
			switch l.Msg {
			case "message received":
				pods[l.Pod] = true
			case "duplicate suspected":
				d.DupSuspectedCaught++
			}
		}
		d.PodsHandled[len(pods)]++
	}
	if extra := rec.assistantReplies() - len(msgs); extra > 0 {
		d.ExtraAssistant += extra
	}
	if cards > len(msgs) {
		d.ExtraCards += cards - len(msgs)
	} else {
		d.MissingCards += len(msgs) - cards
	}
}
