package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

func runID() string { return time.Now().Format("150405") }

// pass reports a scenario that passed.
func pass(name string) bool {
	fmt.Printf("\n%s: PASS\n", name)
	return true
}

// fail reports a scenario that failed, and why.
func fail(name, why string) bool {
	fmt.Printf("\n%s: FAIL: %s\n", name, why)
	return false
}

// faultVerdict reports a scenario by the number of GOTATO-FAULTs it found.
func faultVerdict(name string, faults int) bool {
	if faults == 0 {
		return pass(name)
	}
	return fail(name, fmt.Sprintf("%d GOTATO-FAULT", faults))
}

// stamper prints elapsed time since a scenario began.
type stamper struct{ start time.Time }

func newStamper() stamper    { return stamper{time.Now()} }
func (s stamper) at() string { return fmt.Sprintf("+%02ds", int(time.Since(s.start).Seconds())) }

// chats makes n thread values for a scenario.
func chats(scenario string, n int) []*thread {
	id := runID()
	var ts []*thread
	for i := range n {
		ts = append(ts, &thread{chat: fmt.Sprintf("oc_%s_%s_%02d", scenario, id, i+1), n: i + 1})
	}
	return ts
}

// waitPods waits until the bot has exactly n pods, all ready and none going away.
func (e *env) waitPods(ctx context.Context, n int, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		if ps, err := e.pods(ctx); err == nil && len(ps) == n && allReady(ps) {
			return nil
		}
		sleep(ctx, time.Second)
	}
	return fmt.Errorf("the bot did not settle at %d ready pods within %s", n, limit)
}

// allReady says every pod of the list (name -> ready) is ready.
func allReady(ps map[string]bool) bool {
	ready := true
	for _, ok := range ps {
		ready = ready && ok
	}
	return ready
}

// How long smoke waits for the card, and how often it looks.
const (
	smokeCardWait = 30 * time.Second
	smokeCardPoll = 500 * time.Millisecond
)

// waitCards waits until the chat has a card, and returns the cards it has at the end.
func (e *env) waitCards(ctx context.Context, chat string) []card {
	var cs []card
	for deadline := time.Now().Add(smokeCardWait); time.Now().Before(deadline); sleep(ctx, smokeCardPoll) {
		if cs, _ = e.cards(ctx, chat); len(cs) > 0 {
			break
		}
	}
	return cs
}

// smoke: one message, one card from one of the pods, the message once in the Transcript with one reply.
func (e *env) smoke(ctx context.Context) bool {
	fmt.Println("== smoke")
	pods, err := e.pods(ctx)
	if err != nil {
		return fail("smoke", err.Error())
	}
	fmt.Printf("bot pods: %d\n", len(pods))
	t := &thread{chat: "oc_smoke_" + runID()}
	m := e.say(ctx, t, "smoke", "")
	if m.err != nil {
		return fail("smoke", m.err.Error())
	}
	fmt.Printf("sent %s, deliveries %+v\n", m.sent.MessageID, m.sent.Deliveries)
	cs := e.waitCards(ctx, t.chat)
	if len(cs) == 0 {
		e.printErrors()
	}
	if len(cs) != 1 {
		return fail("smoke", fmt.Sprintf("want 1 card, got %d", len(cs)))
	}
	c := cs[0]
	fmt.Printf("card: instance %s, session %s, text %q\n", c.Instance, c.Session, c.Text)
	if _, ok := pods[c.Instance]; !ok {
		return fail("smoke", fmt.Sprintf("card footer instance %q is not one of the bot pods", c.Instance))
	}
	rec, err := e.record(ctx, t.session())
	if err != nil {
		return fail("smoke", err.Error())
	}
	fmt.Printf("Transcript: %q in %d user message(s), %d assistant reply(ies), %d run(s)\n", m.tag, rec.count(m.tag), rec.assistantReplies(), len(rec.Runs))
	if rec.count(m.tag) != 1 || rec.assistantReplies() != 1 {
		return fail("smoke", "Transcript does not hold the message once with one reply")
	}
	if faults := recordFaults(rec); len(faults) > 0 {
		return fail("smoke", strings.Join(faults, "; "))
	}
	return pass("smoke")
}

// The shape of s3: the pods it starts with and ends with, its threads, the threads with a Run in flight at the
// first scale, the replies into running threads while the pods drain and while they start, the threads that
// have started before the scale-up, the rounds of replies on five pods, and the pauses and waits.
const (
	s3PodsAtStart = 3
	s3PodsAtEnd   = 5
	s3Threads     = 12
	s3InFlight    = 8
	s3DownReplies = 4
	s3UpReplyFrom = 4
	s3Started     = 10
	s3Rounds      = 2
	s3Stagger     = 700 * time.Millisecond
	s3RoundGap    = 3 * time.Second
	s3PodsWait    = 120 * time.Second
	s3Quiet       = 6 * time.Second
	s3Settle      = 90 * time.Second
	s3FinalQuiet  = 15 * time.Second
	instanceChars = 8 // how much of a Runner instance ID names a pod that no log knows
)

// The shape of s4: its threads, the messages of each phase, the pause between messages in a row, and how long
// the Run holds the Claim and how long a thread is quiet.
const (
	s4Threads    = 3
	s4Burst      = 3
	s4Row        = 4
	s4Race       = 4
	s4RowGap     = 150 * time.Millisecond
	s4Holds      = 1500 * time.Millisecond
	s4Quiet      = 6 * time.Second
	s4Settle     = 60 * time.Second
	s4FinalQuiet = 15 * time.Second
	s4FinalWait  = 120 * time.Second
)

// The shape of s2: its threads, how long it looks for the writer, the Liveness TTL and its renewal, and how long
// it waits.
const (
	s2Threads   = 6
	s2FindWait  = 20 * time.Second
	s2MidAfter  = 5 * time.Second  // a reply this long after the kill is inside the Liveness TTL
	s2LateAfter = 11 * time.Second // a reply this long after the kill is after it
	s2Quiet     = 25 * time.Second
	s2Settle    = 100 * time.Second
)

// The shape of s1: its threads, how many start before the restart, from which thread a reply goes into the
// thread two behind, up to which thread a late reply goes, the Run's delay, the pauses, and how long it waits.
const (
	s1Threads   = 12
	s1Early     = 6
	s1QueueFrom = 8
	s1LateTo    = 9
	s1Delay     = "[quirk:delay=8s]"
	s1Stagger   = 700 * time.Millisecond
	s1LateGap   = 500 * time.Millisecond
	s1Quiet     = 25 * time.Second
	s1Settle    = 90 * time.Second
)

// s1: a rolling deploy while Runs are in flight.
func (e *env) s1(ctx context.Context) bool {
	fmt.Println("== s1: rolling deploy with Runs in flight")
	id := runID()
	var ts []*thread
	for i := range s1Threads {
		ts = append(ts, &thread{chat: fmt.Sprintf("oc_s1_%s_%02d", id, i+1)})
	}
	start := time.Now()
	at := func() string { return fmt.Sprintf("+%02ds", int(time.Since(start).Seconds())) }
	for i := range s1Early {
		m := e.say(ctx, ts[i], fmt.Sprintf("s1-%02d-a", i+1), s1Delay)
		fmt.Printf("%s new thread %d: %s\n", at(), i+1, shortDeliveries(m))
		sleep(ctx, s1Stagger)
	}
	fmt.Printf("%s kubectl rollout restart deployment/larkbot\n", at())
	if out, err := e.kubectl(ctx, "rollout", "restart", "deployment/larkbot"); err != nil {
		return fail("s1", out)
	}
	rolled := e.rolloutStatus(ctx, "larkbot")
	e.s1DuringRollout(ctx, ts, at)
	if err := <-rolled; err != nil {
		fmt.Println("rollout:", err)
	}
	fmt.Printf("%s rollout done\n", at())
	for i := s1Early; i < s1LateTo; i++ { // threads whose writer pod is gone
		m := e.say(ctx, ts[i], fmt.Sprintf("s1-%02d-c", i+1), s1Delay)
		fmt.Printf("%s late reply in thread %d: %s\n", at(), i+1, shortDeliveries(m))
		sleep(ctx, s1LateGap)
	}
	fmt.Printf("%s waiting for the Runs to settle\n", at())
	e.waitQuiet(ctx, ts, s1Quiet, s1Settle)
	fmt.Printf("\n%s results\n", at())
	faults := e.report(ctx, ts)
	return faultVerdict("s1", faults)
}

// s1DuringRollout sends while the bot rolls: new threads, replies into threads whose Run is still going, and
// replies in the first threads.
func (e *env) s1DuringRollout(ctx context.Context, ts []*thread, at func() string) {
	for i := s1Early; i < s1Threads; i++ {
		sleep(ctx, time.Second)
		m := e.say(ctx, ts[i], fmt.Sprintf("s1-%02d-a", i+1), s1Delay)
		fmt.Printf("%s new thread %d: %s\n", at(), i+1, shortDeliveries(m))
		if i >= s1QueueFrom { // a reply into a thread whose Run is still going: queued, or forwarded to its writer
			m := e.say(ctx, ts[i-2], fmt.Sprintf("s1-%02d-q", i-1), s1Delay)
			fmt.Printf("%s reply into the running thread %d: %s\n", at(), i-1, shortDeliveries(m))
		}
	}
	for i := range s1Early { // replies in threads whose Run is in flight or just ended
		sleep(ctx, time.Second)
		m := e.say(ctx, ts[i], fmt.Sprintf("s1-%02d-b", i+1), s1Delay)
		fmt.Printf("%s reply in thread %d: %s\n", at(), i+1, shortDeliveries(m))
	}
}

// rolloutStatus waits for the rollout of a Deployment in the background; the channel gets its error.
func (e *env) rolloutStatus(ctx context.Context, deployment string) <-chan error {
	rolled := make(chan error, 1)
	go func() {
		out, err := e.kubectl(ctx, "rollout", "status", "deployment/"+deployment, "--timeout=170s")
		if err != nil {
			err = fmt.Errorf("%w: %s", err, out)
		}
		rolled <- err
	}()
	return rolled
}

func shortDeliveries(m *msg) string {
	if m.err != nil {
		return "send error: " + m.err.Error()
	}
	var parts []string
	for _, d := range m.sent.Deliveries {
		ep := d.Endpoint
		if i := strings.LastIndex(ep, "//"); i >= 0 {
			ep = ep[i+2:]
		}
		res := fmt.Sprint(d.Status)
		if d.Error != "" {
			res = "error"
		}
		parts = append(parts, ep+" "+res)
	}
	return strings.Join(parts, " -> ")
}

// holder is the Instance that holds the Session's Claim now ("" when none).
func holder(r record) string {
	h := ""
	for _, ev := range r.Events {
		switch ev.Kind {
		case "session_claimed":
			h = ev.Instance
		case "session_released":
			h = ""
		}
	}
	return h
}

// podOf maps a Runner instance ID to the pod that logged it at start.
func (e *env) podOf(instance string) string {
	for _, l := range e.readLogs() {
		if l.Msg == "runner ready" && l.KV["runner_instance"] == instance {
			return l.KV["instance"]
		}
	}
	return ""
}

// s2: kill the pod that writes a Run, mid-Run.
func (e *env) s2(ctx context.Context) bool {
	fmt.Println("== s2: kill the writer mid-Run")
	const delay = "[quirk:delay=15s]"
	id := runID()
	var ts []*thread
	for i := range s2Threads {
		ts = append(ts, &thread{chat: fmt.Sprintf("oc_s2_%s_%02d", id, i+1)})
	}
	start := time.Now()
	at := func() string { return fmt.Sprintf("+%02ds", int(time.Since(start).Seconds())) }
	for i, t := range ts {
		m := e.say(ctx, t, fmt.Sprintf("s2-%02d-a", i+1), delay)
		fmt.Printf("%s new thread %d: %s\n", at(), i+1, shortDeliveries(m))
	}
	// Find the writer of the first thread, once its Run holds the Claim.
	victim, victimInst := e.s2FindWriter(ctx, ts)
	if victim == "" {
		e.printErrors()
		return fail("s2", "could not find the pod that writes thread 1")
	}
	onVictim, killAt, err := e.s2Kill(ctx, ts, victim, victimInst, at)
	if err != nil {
		return fail("s2", "kill failed")
	}
	e.s2Replies(ctx, onVictim, killAt, at)
	fmt.Printf("%s waiting for the Runs to settle\n", at())
	e.waitQuiet(ctx, ts, s2Quiet, s2Settle)

	fmt.Printf("\n%s results\n", at())
	for _, t := range onVictim {
		r, err := e.record(ctx, t.session())
		if err != nil {
			continue
		}
		fmt.Printf("  %s (first thread: %v)\n", t.session(), t == ts[0])
		s2PrintTakeover(r, victimInst, killAt)
		e.s2PrintCards(ctx, t, r, start)
	}
	fmt.Println()
	faults := e.report(ctx, ts)
	return faultVerdict("s2", faults)
}

// s2FindWriter waits for the Run of the first thread to hold the Claim, and returns the pod and the instance
// that hold it.
func (e *env) s2FindWriter(ctx context.Context, ts []*thread) (victim, victimInst string) {
	for deadline := time.Now().Add(s2FindWait); time.Now().Before(deadline) && victim == ""; sleep(ctx, time.Second) {
		if r, err := e.record(ctx, ts[0].session()); err == nil {
			if victimInst = holder(r); victimInst != "" {
				victim = e.podOf(victimInst)
			}
		}
	}
	return victim, victimInst
}

// s2Kill finds the threads the writer holds, then kills the writer.
func (e *env) s2Kill(ctx context.Context, ts []*thread, victim, victimInst string, at func() string) ([]*thread, time.Time, error) {
	var onVictim []*thread
	for _, t := range ts {
		if r, err := e.record(ctx, t.session()); err == nil && holder(r) == victimInst {
			onVictim = append(onVictim, t)
		}
	}
	fmt.Printf("%s writer of thread 1 is pod %s (instance %.8s); it writes %d of the 6 threads\n", at(), victim, victimInst, len(onVictim))

	e.victimPod = victim
	e.victimIP, _ = e.kubectl(ctx, "get", "pod", victim, "-o", "jsonpath={.status.podIP}")
	killAt := time.Now()
	out, err := e.kill(ctx, victim)
	fmt.Printf("%s killed %s (%s): %s\n", at(), victim, e.killMode, out)
	if err != nil {
		return nil, killAt, err
	}
	return onVictim, killAt, nil
}

// s2Replies sends a message into each thread the writer held: at once, inside the Liveness TTL and after it.
func (e *env) s2Replies(ctx context.Context, onVictim []*thread, killAt time.Time, at func() string) {
	for i, t := range onVictim {
		m := e.say(ctx, t, fmt.Sprintf("s2-r1-%d", i+1), "")
		fmt.Printf("%s immediate reply in %s: %s\n", at(), t.session(), shortDeliveries(m))
	}
	sleep(ctx, time.Until(killAt.Add(s2MidAfter)))
	for i, t := range onVictim {
		m := e.say(ctx, t, fmt.Sprintf("s2-mid-%d", i+1), "")
		fmt.Printf("%s reply inside the Liveness TTL in %s: %s\n", at(), t.session(), shortDeliveries(m))
	}
	sleep(ctx, time.Until(killAt.Add(s2LateAfter))) // the Liveness TTL is 10 s, renewed every third of it
	for i, t := range onVictim {
		m := e.say(ctx, t, fmt.Sprintf("s2-r2-%d", i+1), "")
		fmt.Printf("%s reply after the Liveness TTL in %s: %s\n", at(), t.session(), shortDeliveries(m))
	}
}

// s2PrintTakeover prints the first claim after the kill by another instance, and the events after it.
func s2PrintTakeover(r record, victimInst string, killAt time.Time) {
	var took time.Duration
	after := false
	var tail []string
	for _, ev := range r.Events {
		if ev.Kind == "session_claimed" && ev.Instance != victimInst && ev.From == victimInst && !after {
			after = true
			took = ev.Time.Sub(killAt)
			fmt.Printf("    takeover: claimed by %.8s (why=%s epoch=%d) %s after the kill\n", ev.Instance, ev.Why, ev.Epoch, took.Round(100*time.Millisecond))
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

// s2PrintCards prints the thread's cards and the messages in its Record the driver did not send.
func (e *env) s2PrintCards(ctx context.Context, t *thread, r record, start time.Time) {
	cs, _ := e.cards(ctx, t.chat)
	for _, c := range cs {
		fmt.Printf("    card +%02ds from %s: %q\n", int(c.Time.Sub(start).Seconds()), c.Instance, trunc(c.Text, 50))
	}
	for i := range r.Messages {
		if r.Messages[i].Role != "assistant" && !strings.Contains(r.text(i), "T[") {
			fmt.Printf("    non-driver message (%s): %q\n", r.Messages[i].Role, trunc(r.text(i), 120))
		}
	}
}

// kill stops the writer's pod. delete: kubectl delete pod --grace-period=0 --force (SIGTERM, then SIGKILL at once;
// the pod is replaced). crash: SIGKILL the container with no SIGTERM, through crictl on the kind node; kubelet
// restarts the container in the same pod.
func (e *env) kill(ctx context.Context, pod string) (string, error) {
	if e.killMode == "crash" {
		node := "gotato-control-plane"
		ids, err := exec.CommandContext(ctx, "docker", "exec", node, "crictl", "ps", "-q", "--name", "larkbot", "--label", "io.kubernetes.pod.name="+pod).Output() //nolint:gosec // arguments are pod names from kubectl
		id := strings.TrimSpace(string(ids))
		if err != nil {
			return "crictl ps failed", fmt.Errorf("crictl ps: %w", err)
		}
		if id == "" {
			return "no container found", fmt.Errorf("crictl ps: no running larkbot container in pod %s", pod)
		}
		out, err := exec.CommandContext(ctx, "docker", "exec", node, "crictl", "stop", "-t", "0", id).CombinedOutput() //nolint:gosec // id comes from crictl
		return strings.TrimSpace(string(out)), err
	}
	out, err := e.kubectl(ctx, "delete", "pod", pod, "--grace-period=0", "--force")
	return lastLine(out), err
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

func eventNote(ev recordEvent) string {
	switch {
	case ev.Status != "":
		return "(" + ev.Status + ")"
	case ev.Why != "":
		return "(" + ev.Why + ")"
	}
	return ""
}

func trunc(s string, n int) string {
	if r := []rune(strings.ReplaceAll(s, "\n", " ")); len(r) > n {
		return string(r[:n]) + "..."
	}
	return strings.ReplaceAll(s, "\n", " ")
}
