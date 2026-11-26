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

// quietPoll is how often waitQuiet looks at the threads.
const quietPoll = 2 * time.Second

const (
	gapKnown = "KNOWN-BOT-GAP"
	fault    = "GOTATO-FAULT"
	unexpl   = "UNEXPLAINED"
)

// say sends one message into a thread (the first one starts it).
func (e *env) say(ctx context.Context, t *thread, tag, directive string) *msg {
	m := &msg{tag: "T[" + tag + "]"}
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
		total, busy := e.quietSample(ctx, ts)
		if total != last {
			last, lastChange = total, time.Now()
		}
		if busy == 0 && time.Since(lastChange) >= quiet {
			return
		}
		sleep(ctx, quietPoll)
	}
}

// quietSample counts the cards of the threads and the Runs in flight in them.
func (e *env) quietSample(ctx context.Context, ts []*thread) (total, busy int) {
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
	return total, busy
}

// outcome of a message that is not in its Transcript exactly once, from the bot logs.
func (e *env) classify(m *msg, count int, logs []logLine) (string, string) {
	if count == 1 {
		return "ok", ""
	}
	mine, pods := linesOf(m, logs)
	if count > 1 {
		return classifyDup(m, count, mine, pods)
	}
	// count == 0
	if m.err != nil || !m.sent.acked() {
		return gapKnown, "never acked by a bot (no 2xx in any delivery)"
	}
	return e.classifyMissing(mine)
}

// linesOf returns the bot log lines about the message, and the pods that logged receiving it.
func linesOf(m *msg, logs []logLine) ([]logLine, map[string]bool) {
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
	return mine, pods
}

// classifyDup is the outcome of a message that is in its Transcript more than once.
func classifyDup(m *msg, count int, mine []logLine, pods map[string]bool) (string, string) {
	dup := false
	for _, l := range mine {
		dup = dup || l.Msg == "duplicate suspected"
	}
	if len(m.sent.Deliveries) > 1 || dup {
		return gapKnown, fmt.Sprintf("duplicate from redelivery (%d deliveries, on %d pods)", len(m.sent.Deliveries), len(pods))
	}
	return unexpl, fmt.Sprintf("in the Transcript %d times, delivered once", count)
}

// classifyMissing is the outcome of an acked message that is not in its Transcript, from its log lines.
func (e *env) classifyMissing(mine []logLine) (string, string) {
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
		return sendRefusal(mine)
	case has("create session"):
		return gapKnown, "create session failed"
	case has("send not_writer, forwarding"):
		return gapKnown, "forwarding, no outcome logged (pod gone)"
	case has("message received"):
		return gapKnown, "received, no outcome logged (pod gone before Send returned)"
	}
	return unexpl, "acked, but no pod logged it"
}

// sendRefusal is the outcome of a message whose Send the bot logged as an error, by that error.
func sendRefusal(mine []logLine) (string, string) {
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
	rc := reportCounts{cat: map[string]int{}, byReason: map[string]int{}}
	for _, t := range ts {
		if t.root == "" {
			continue
		}
		e.reportThread(ctx, &rc, t, logs)
	}
	cat, faults, byReason := rc.cat, rc.faults, rc.byReason
	sentN, ackedN, onceN, missingN, dupN := rc.sentN, rc.ackedN, rc.onceN, rc.missingN, rc.dupN
	cardN, noticeN, runN, dupCards, lostCards := rc.cardN, rc.noticeN, rc.runN, rc.dupCards, rc.lostCards
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

// reportCounts is what report counts over the threads, for its totals.
type reportCounts struct {
	cat                                                                             map[string]int
	faults                                                                          int
	sentN, ackedN, onceN, missingN, dupN, cardN, noticeN, runN, dupCards, lostCards int
	byReason                                                                        map[string]int
}

// reportThread checks one thread against its Record, the cards and the logs, prints one line per problem, and
// counts into rc.
func (e *env) reportThread(ctx context.Context, rc *reportCounts, t *thread, logs []logLine) {
	rec, err := e.record(ctx, t.session())
	if err != nil { // no Session: the first message never got in; each message is classified from the logs
		rec = record{}
	}
	for _, f := range recordFaults(rec) {
		rc.faults++
		fmt.Printf("  %s %s: %s\n", fault, t.session(), f)
	}
	for _, m := range t.msgs {
		e.reportMsg(rc, t, m, rec, logs)
	}
	cs, _ := e.cards(ctx, t.chat)
	shown := 0
	for _, c := range cs {
		if strings.Contains(c.Text, busyNoticePrefix) {
			rc.noticeN++
		} else {
			shown++
		}
	}
	rc.cardN += shown
	rc.runN += len(rec.Runs)
	if shown > len(rec.Runs) {
		rc.dupCards += shown - len(rec.Runs)
	} else {
		rc.lostCards += len(rec.Runs) - shown
	}
}

// reportMsg classifies one message of the thread, counts it into rc, and prints it when it is a fault or
// unexplained.
func (e *env) reportMsg(rc *reportCounts, t *thread, m *msg, rec record, logs []logLine) {
	rc.sentN++
	if m.err == nil && m.sent.acked() {
		rc.ackedN++
	}
	n := rec.count(m.tag)
	c, why := e.classify(m, n, logs)
	switch n {
	case 1:
		rc.onceN++
	case 0:
		rc.missingN++
	default:
		rc.dupN++
	}
	if c == "ok" {
		return
	}
	rc.cat[c]++
	rc.byReason[c+": "+why]++
	if c == fault {
		rc.faults++
	}
	if c == fault || c == unexpl {
		fmt.Printf("  %s %s %s: %s\n", c, t.session(), m.tag, why)
	}
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
		d.addMessage(m, rec, logs)
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

// addMessage counts one sent message: its deliveries, its copies in the Transcript and the pods that handled it.
func (d *dupStats) addMessage(m *msg, rec record, logs []logLine) {
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
	d.PodsHandled[d.podsHandling(m, logs)]++
}

// podsHandling counts the pods that logged receiving the message, and the redeliveries a seen-set caught.
func (d *dupStats) podsHandling(m *msg, logs []logLine) int {
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
	return len(pods)
}
