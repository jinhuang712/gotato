package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// reportStoreErrors prints the store errors of the bot logs, each a GOTATO-FAULT, and returns how many there are.
func (e *env) reportStoreErrors() (faults int) {
	for _, s := range storeErrors(e.readLogs()) {
		faults++
		fmt.Printf("  %s store error in a bot log: %s\n", fault, s)
	}
	return faults
}

// unansweredThreads counts the threads whose Transcript ends on a user message.
func (e *env) unansweredThreads(ctx context.Context, ts []*thread) (n int) {
	for _, t := range ts {
		if r, err := e.record(ctx, t.session()); err == nil && r.unanswered() {
			n++
			fmt.Printf("  %s: the Transcript ends on a user message (no reply after it)\n", t.session())
		}
	}
	return n
}

// s3: scale the bot Deployment 3 -> 1 -> 5 while Runs are in flight and between messages.
func (e *env) s3(ctx context.Context) bool {
	fmt.Println("== s3: scale the bot 3 -> 1 -> 5 with Runs in flight and between messages")
	const delay = "[quirk:delay=8s]"
	st := newStamper()
	before, err := e.pods(ctx)
	if err != nil || len(before) != s3PodsAtStart {
		return verdict("s3", false, fmt.Sprintf("want 3 bot pods to start with, have %v (%v)", podNames(before), err))
	}
	ts := chats("s3", s3Threads)
	say := func(i int, tag, d, what string) {
		m := e.say(ctx, ts[i], tag, d)
		fmt.Printf("%s %s thread %d: %s\n", st.at(), what, i+1, shortDeliveries(m))
	}

	fmt.Printf("%s 3 pods: %v\n", st.at(), podNames(before))
	run := &s3Run{e: e, st: st, ts: ts, say: say, delay: delay}
	if err := run.scaleDown(ctx); err != nil {
		return verdict("s3", false, err.Error())
	}
	five, err := run.scaleUp(ctx)
	if err != nil {
		return verdict("s3", false, err.Error())
	}

	fmt.Printf("\n%s results\n", st.at())
	faults := e.report(ctx, ts)
	faults += e.reportStoreErrors()
	unanswered := e.unansweredThreads(ctx, ts)
	fmt.Printf("  threads whose Transcript ends on an unanswered user message: %d\n", unanswered)

	claimsByNew := run.claims(ctx, before, five)
	if claimsByNew == 0 {
		return verdict("s3", false, "no pod added by the scale-up claimed a Session")
	}
	return verdict("s3", faults == 0, fmt.Sprintf("%d GOTATO-FAULT", faults))
}

// s3Run is one play of s3: the driver, the stamper, the threads, the Run's delay and the sender into a thread.
type s3Run struct {
	e     *env
	st    stamper
	ts    []*thread
	say   func(i int, tag, d, what string)
	delay string
}

// scaleDown scales the bot 3 -> 1 with Runs in flight, sends into the threads while the pods drain, and waits
// for the Runs to settle.
func (r *s3Run) scaleDown(ctx context.Context) error {
	e, st, ts, say, delay := r.e, r.st, r.ts, r.say, r.delay
	for i := range s3InFlight {
		say(i, fmt.Sprintf("s3-%02d-a", i+1), delay, "new")
	}
	sleep(ctx, time.Second)
	fmt.Printf("%s kubectl scale --replicas=1 (threads 1-8 have Runs in flight)\n", st.at())
	if err := e.scale(ctx, 1); err != nil {
		return err
	}
	for i := range s3DownReplies { // into running threads, while pods drain
		sleep(ctx, s3Stagger)
		say(i, fmt.Sprintf("s3-%02d-q", i+1), delay, "reply into the running")
	}
	for i := s3InFlight; i < s3Started; i++ {
		sleep(ctx, s3Stagger)
		say(i, fmt.Sprintf("s3-%02d-a", i+1), delay, "new")
	}
	if err := e.waitPods(ctx, 1, s3PodsWait); err != nil {
		return err
	}
	one, _ := e.pods(ctx)
	fmt.Printf("%s 1 pod: %v\n", st.at(), podNames(one))
	fmt.Printf("%s waiting for the Runs to settle (between messages)\n", st.at())
	e.waitQuiet(ctx, ts, s3Quiet, s3Settle)
	return nil
}

// scaleUp sends into every thread, scales the bot 1 -> 5 with Runs in flight, and sends on 5 pods; it
// returns the 5 pods.
func (r *s3Run) scaleUp(ctx context.Context) (map[string]bool, error) {
	e, st, ts, say, delay := r.e, r.st, r.ts, r.say, r.delay
	for i := range ts[:s3Started] { // a message in every thread, no Run going
		say(i, fmt.Sprintf("s3-%02d-b", i+1), delay, "reply after the Run ended in")
	}
	sleep(ctx, time.Second)
	fmt.Printf("%s kubectl scale --replicas=5 (Runs in flight on the one pod)\n", st.at())
	if err := e.scale(ctx, s3PodsAtEnd); err != nil {
		return nil, err
	}
	for i := s3UpReplyFrom; i < s3InFlight; i++ { // while the new pods start and become ready
		sleep(ctx, time.Second)
		say(i, fmt.Sprintf("s3-%02d-c", i+1), delay, "reply into the running")
	}
	if err := e.waitPods(ctx, s3PodsAtEnd, s3PodsWait); err != nil {
		return nil, err
	}
	five, _ := e.pods(ctx)
	fmt.Printf("%s 5 pods: %v\n", st.at(), podNames(five))
	e.waitQuiet(ctx, ts, s3Quiet, s3Settle)
	for i := s3Started; i < s3Threads; i++ {
		say(i, fmt.Sprintf("s3-%02d-a", i+1), "", "new")
	}
	for round := range s3Rounds { // short Runs, every thread: the new pods are in the draw now
		for i := range ts {
			say(i, fmt.Sprintf("s3-%02d-d%d", i+1, round+1), "", "reply on 5 pods in")
		}
		sleep(ctx, s3RoundGap)
	}
	fmt.Printf("%s waiting for the Runs to settle\n", st.at())
	e.waitQuiet(ctx, ts, s3FinalQuiet, s3Settle)
	return five, nil
}

// claims prints the claims of Sessions by pod, and returns those by pods that did not exist at the start.
func (r *s3Run) claims(ctx context.Context, before, five map[string]bool) int {
	e, ts := r.e, r.ts
	// Which pods served the Sessions: the last claim of each Session, by pod.
	orig := map[string]bool{}
	for p := range before {
		orig[p] = true
	}
	served := map[string]int{}
	claimsByNew := 0
	for _, t := range ts {
		rec, err := e.record(ctx, t.session())
		if err != nil {
			continue
		}
		claimsByNew += e.s3ClaimsOf(rec, orig, served)
	}
	var parts []string
	for _, p := range podNames(five) {
		tag := "old"
		if !orig[p] {
			tag = "new"
		}
		parts = append(parts, fmt.Sprintf("%s(%s) %d", p, tag, served[p]))
	}
	fmt.Printf("  claims of Sessions by pod, over the whole run: %s\n", strings.Join(parts, ", "))
	fmt.Printf("  claims by pods that did not exist at the start: %d\n", claimsByNew)
	return claimsByNew
}

// s3ClaimsOf counts the claims in one Record into served, by pod, and returns those by pods not in orig.
func (e *env) s3ClaimsOf(r record, orig map[string]bool, served map[string]int) (claimsByNew int) {
	for _, ev := range r.Events {
		if ev.Kind != "session_claimed" {
			continue
		}
		p := e.podOf(ev.Instance)
		if p == "" {
			p = "?" + ev.Instance[:instanceChars]
		}
		served[p]++
		if !orig[p] {
			claimsByNew++
		}
	}
	return claimsByNew
}

// s4: bots race for the same Session. Every burst goes into one Lark thread; fakelark delivers each message
// to a random pod, so the pods contend for the one Session.
func (e *env) s4(ctx context.Context) bool {
	fmt.Println("== s4: bots contend for the same Session")
	st := newStamper()
	ts := chats("s4", s4Threads)
	var out sync.Mutex
	logf := func(i int, format string, a ...any) {
		out.Lock()
		defer out.Unlock()
		fmt.Printf("%s thread %d: %s\n", st.at(), i+1, fmt.Sprintf(format, a...))
	}
	seq := make([][]string, len(ts)) // per thread: the tags sent one after the other, in order
	var wg sync.WaitGroup
	for i, t := range ts {
		wg.Go(func() {
			e.s4Thread(ctx, i, t, seq, logf)
		})
	}
	wg.Wait()
	fmt.Printf("%s waiting for the Runs to settle\n", st.at())
	e.waitQuiet(ctx, ts, s4FinalQuiet, s4FinalWait)

	fmt.Printf("\n%s results\n", st.at())
	faults := e.report(ctx, ts)
	faults += e.reportStoreErrors()
	logs := e.readLogs()
	count := func(msg string) (n int) {
		for _, l := range logs {
			if l.Msg == msg {
				n++
			}
		}
		return n
	}
	fmt.Printf("  bot log lines: send queued %d, send ran a new run %d, not_writer forwarding %d, forwarded %d, not run will retry %d, delivered after retry %d, delivery unknown %d, delivery failed %d\n",
		count("send queued"), count("send ran a new run"), count("send not_writer, forwarding"), count("forwarded"),
		count("not run, will retry"), count("delivered after retry"), count("delivery unknown"), count("delivery failed, not run"))
	readFaults, inv := e.s4Results(ctx, ts, seq)
	faults += readFaults
	if inv > 0 {
		fmt.Printf("  KNOWN-BOT-GAP %d message(s) sent in a row landed in the Transcript out of order (two pods, no cross-pod order)\n", inv)
	}
	return verdict("s4", faults == 0, fmt.Sprintf("%d GOTATO-FAULT", faults))
}

// s4Thread plays s4's phases in one thread: a root message, a burst into its Run, messages in a row, and a
// burst with no Run going.
func (e *env) s4Thread(ctx context.Context, i int, t *thread, seq [][]string, logf func(int, string, ...any)) {
	n := i + 1
	burst := func(phase string, count int, directive string) {
		var bw sync.WaitGroup
		res := make([]*msg, count)
		for j := range count {
			bw.Go(func() { res[j] = e.say(ctx, t, fmt.Sprintf("s4-%02d-%s%d", n, phase, j+1), directive) })
		}
		bw.Wait()
		var ds []string
		for _, m := range res {
			ds = append(ds, shortDeliveries(m))
		}
		logf(i, "burst %s of %d at once: %s", phase, count, strings.Join(ds, " | "))
	}
	sequential := func(phase string, count int, gap time.Duration, directive string) {
		var ds []string
		for j := range count {
			tag := fmt.Sprintf("s4-%02d-%s%d", n, phase, j+1)
			m := e.say(ctx, t, tag, directive)
			seq[i] = append(seq[i], m.tag)
			ds = append(ds, shortDeliveries(m))
			sleep(ctx, gap)
		}
		logf(i, "%d in a row %s: %s", count, phase, strings.Join(ds, " | "))
	}
	// No Run takes more than 4 messages into its FollowUp buffer (limit_exceeded, a known bound), so
	// each phase sends at most 4 while one Run is going; the phases are about who writes.
	quiet := func() { e.waitQuiet(ctx, []*thread{t}, s4Quiet, s4Settle) }
	m := e.say(ctx, t, fmt.Sprintf("s4-%02d-root", n), "[quirk:delay=6s]")
	logf(i, "root: %s", shortDeliveries(m))
	sleep(ctx, s4Holds)     // the Run holds the Claim now
	burst("b", s4Burst, "") // into the Run in flight, from random pods: queued here, or forwarded
	quiet()
	sequential("s", s4Row, s4RowGap, "[quirk:delay=3s]") // the first starts a Run, the rest join it
	quiet()
	burst("r", s4Race, "[quirk:delay=3s]") // no Run going: the pods race to start one
	quiet()
}

// s4Results prints what each thread's Record holds; it returns the Records it could not read (GOTATO-FAULTs) and
// the messages sent in a row that landed out of order.
func (e *env) s4Results(ctx context.Context, ts []*thread, seq [][]string) (int, int) {
	faults := 0
	inv := 0
	for i, t := range ts {
		r, err := e.record(ctx, t.session())
		if err != nil {
			faults++
			fmt.Printf("  %s %s: cannot read the Record: %v\n", fault, t.session(), err)
			continue
		}
		in := r.inversions(seq[i])
		inv += in
		kinds := map[string]int{}
		holders := map[string]bool{}
		for _, ev := range r.Events {
			kinds[ev.Kind]++
			if ev.Kind == "session_claimed" {
				holders[ev.Instance] = true
			}
		}
		fmt.Printf("  thread %d: %d messages sent, %d user in the Transcript, %d assistant, %d Runs, %d claims by %d Instances, %d reordered of %d sent in a row, ends on a user message: %v\n",
			i+1, len(t.msgs), countRole(r, "user"), r.assistantReplies(), len(r.Runs), kinds["session_claimed"], len(holders), in, len(seq[i]), r.unanswered())
	}
	return faults, inv
}

// The shape of s5: its threads, how many have a Run in flight when the redelivery arrives, how many have a quick
// Run, the threads of the first wave (both), the Run's delay, and how long it waits after each wave.
const (
	s5Threads   = 20
	s5Inflight  = 6
	s5Quick     = 8
	s5FirstWave = s5Inflight + s5Quick
	s5Delay     = "[quirk:delay=8s]"
	s5Quiet1    = 10 * time.Second
	s5Settle1   = 90 * time.Second
	s5Quiet2    = 15 * time.Second
	s5Settle2   = 120 * time.Second
)

// s5: the bots ack late, so fakelark redelivers the same event to other pods. It measures, and does not judge:
// the bot has no key that tells a redelivery from a new message.
func (e *env) s5(ctx context.Context) bool {
	fmt.Println("== s5: late ack, so the same event (same message_id) is delivered again")
	d := e.ackDelay(ctx)
	if d == "" || d == "0s" || d == "0" {
		fmt.Println("the bots run with -ack-delay=0; start them with: ACK_DELAY=4s cluster/run.sh up")
		return verdict("s5", false, "the bot's -ack-delay is "+fmt.Sprintf("%q", d)+" (needs more than fakelark's 3 s ack timeout)")
	}
	fmt.Printf("bots' -ack-delay=%s, fakelark's ack timeout 3s, up to 3 redeliveries\n", d)
	st := newStamper()
	ts := chats("s5", s5Threads)
	inflight, fresh := ts[:s5Inflight], ts[s5Inflight:] // inflight: a Run of 8 s is going when the redelivery arrives
	e.s5Send(ctx, st, ts, inflight, fresh)

	fmt.Printf("\n%s results\n", st.at())
	faults := e.report(ctx, ts)
	faults += e.reportStoreErrors()
	all := e.s5Dups(ctx, ts, inflight, fresh)
	fmt.Println("\nDESIGN INPUT (KNOWN-BOT-GAP, not a Gotato fault): a redelivery that lands on a pod that has not seen the message_id is handled as a new message.")
	fmt.Printf("  duplicate user messages in Transcripts: %d, duplicate reply cards: %d, duplicate assistant replies: %d\n", all.ExtraUserCopies, all.ExtraCards, all.ExtraAssistant)
	return verdict("s5", faults == 0, fmt.Sprintf("%d GOTATO-FAULT", faults))
}

// s5Send plays the two waves of s5: the first message of each thread, then a second message in the threads of
// the first wave and the first message of the threads that are left.
func (e *env) s5Send(ctx context.Context, st stamper, ts, inflight, fresh []*thread) {
	var wg sync.WaitGroup
	for i, t := range inflight {
		wg.Go(func() { e.say(ctx, t, fmt.Sprintf("s5-%02d-a", i+1), s5Delay) })
	}
	for i, t := range fresh[:s5Quick] { // a quick Run, over before the redelivery arrives
		wg.Go(func() { e.say(ctx, t, fmt.Sprintf("s5-%02d-a", i+s5Inflight+1), "") })
	}
	wg.Wait() // every send returns after its last delivery
	fmt.Printf("%s first messages sent (%d threads)\n", st.at(), 14)
	e.waitQuiet(ctx, ts[:s5FirstWave], s5Quiet1, s5Settle1)
	// A second message in each of the 14 threads, now idle, and 6 new threads' messages.
	for i, t := range ts[:s5FirstWave] {
		wg.Go(func() { e.say(ctx, t, fmt.Sprintf("s5-%02d-b", i+1), "") })
	}
	for i, t := range fresh[s5Quick:] {
		wg.Go(func() { e.say(ctx, t, fmt.Sprintf("s5-%02d-a", i+s5FirstWave+1), "") })
	}
	wg.Wait()
	fmt.Printf("%s second messages sent\n", st.at())
	e.waitQuiet(ctx, ts, s5Quiet2, s5Settle2)
}

// s5Dups prints what the Records, the cards and the logs say about duplicates, by group of threads, and returns
// the totals over all threads.
func (e *env) s5Dups(ctx context.Context, ts, inflight, fresh []*thread) dupStats {
	logs := e.readLogs()
	groups := []struct {
		name string
		ts   []*thread
	}{
		{"threads 1-6: first message a Run of 8 s (in flight when redelivered), then one more message", inflight},
		{"threads 7-14: first message a quick Run, then one more message", fresh[:s5Quick]},
		{"threads 15-20: one message, a quick Run", fresh[s5Quick:]},
		{"all threads", ts},
	}
	var all dupStats
	fmt.Println()
	for _, g := range groups {
		var s dupStats
		for _, t := range g.ts {
			rec, _ := e.record(ctx, t.session())
			s.addThread(t.msgs, rec, e.replyCards(ctx, t.chat), logs)
		}
		printDup(g.name, s)
		if g.name == "all threads" {
			all = s
		}
	}
	return all
}

func printDup(name string, s dupStats) {
	var keys []int
	for k := range s.Attempts {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	var att []string
	for _, k := range keys {
		att = append(att, fmt.Sprintf("%d deliveries x%d", k, s.Attempts[k]))
	}
	keys = keys[:0]
	for k := range s.PodsHandled {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	var pods []string
	for _, k := range keys {
		pods = append(pods, fmt.Sprintf("%d pod(s) x%d", k, s.PodsHandled[k]))
	}
	fmt.Printf("  %s: %d messages\n", name, s.Messages)
	fmt.Printf("    delivered more than once by fakelark: %d (%d extra deliveries); acked 2xx in time: %d; per message: %s\n", s.DeliveredTwice, s.ExtraDeliveries, s.Acked, strings.Join(att, ", "))
	fmt.Printf("    handled by how many pods: %s; redeliveries a pod's seen-set caught (duplicate suspected): %d\n", strings.Join(pods, ", "), s.DupSuspectedCaught)
	fmt.Printf("    in a Transcript more than once: %d messages (%d extra copies, at most %d copies); extra assistant replies %d; extra reply cards %d; missing cards %d\n",
		s.InTranscriptTwice, s.ExtraUserCopies, s.MaxCopies, s.ExtraAssistant, s.ExtraCards, s.MissingCards)
}
