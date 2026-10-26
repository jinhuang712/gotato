package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

func runID() string { return time.Now().Format("150405") }

func verdict(name string, ok bool, why string) bool {
	if ok {
		fmt.Printf("\n%s: PASS\n", name)
	} else {
		fmt.Printf("\n%s: FAIL: %s\n", name, why)
	}
	return ok
}

// smoke: one message, one card from one of the pods, the message once in the Transcript with one reply.
func (e *env) smoke(ctx context.Context) bool {
	fmt.Println("== smoke")
	pods, err := e.pods(ctx)
	if err != nil {
		return verdict("smoke", false, err.Error())
	}
	fmt.Printf("bot pods: %d\n", len(pods))
	t := &thread{chat: "oc_smoke_" + runID()}
	m := e.say(ctx, t, "smoke", "")
	if m.err != nil {
		return verdict("smoke", false, m.err.Error())
	}
	fmt.Printf("sent %s, deliveries %+v\n", m.sent.MessageID, m.sent.Deliveries)
	var cs []card
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); sleep(ctx, 500*time.Millisecond) {
		if cs, _ = e.cards(ctx, t.chat); len(cs) > 0 {
			break
		}
	}
	if len(cs) == 0 {
		e.printErrors()
	}
	if len(cs) != 1 {
		return verdict("smoke", false, fmt.Sprintf("want 1 card, got %d", len(cs)))
	}
	c := cs[0]
	fmt.Printf("card: instance %s, session %s, text %q\n", c.Instance, c.Session, c.Text)
	if _, ok := pods[c.Instance]; !ok {
		return verdict("smoke", false, fmt.Sprintf("card footer instance %q is not one of the bot pods", c.Instance))
	}
	rec, err := e.record(ctx, t.session())
	if err != nil {
		return verdict("smoke", false, err.Error())
	}
	fmt.Printf("Transcript: %q in %d user message(s), %d assistant reply(ies), %d run(s)\n", m.tag, rec.count(m.tag), rec.assistantReplies(), len(rec.Runs))
	if rec.count(m.tag) != 1 || rec.assistantReplies() != 1 {
		return verdict("smoke", false, "Transcript does not hold the message once with one reply")
	}
	if faults := recordFaults(rec); len(faults) > 0 {
		return verdict("smoke", false, strings.Join(faults, "; "))
	}
	return verdict("smoke", true, "")
}

// s1: a rolling deploy while Runs are in flight.
func (e *env) s1(ctx context.Context) bool {
	fmt.Println("== s1: rolling deploy with Runs in flight")
	const delay = "[quirk:delay=8s]"
	id := runID()
	var ts []*thread
	for i := range 12 {
		ts = append(ts, &thread{chat: fmt.Sprintf("oc_s1_%s_%02d", id, i+1)})
	}
	start := time.Now()
	at := func() string { return fmt.Sprintf("+%02ds", int(time.Since(start).Seconds())) }
	for i := range 6 {
		m := e.say(ctx, ts[i], fmt.Sprintf("s1-%02d-a", i+1), delay)
		fmt.Printf("%s new thread %d: %s\n", at(), i+1, shortDeliveries(m))
		sleep(ctx, 700*time.Millisecond)
	}
	fmt.Printf("%s kubectl rollout restart deployment/larkbot\n", at())
	if out, err := e.kubectl(ctx, "rollout", "restart", "deployment/larkbot"); err != nil {
		return verdict("s1", false, out)
	}
	rolled := make(chan error, 1)
	go func() {
		out, err := e.kubectl(ctx, "rollout", "status", "deployment/larkbot", "--timeout=170s")
		if err != nil {
			err = fmt.Errorf("%w: %s", err, out)
		}
		rolled <- err
	}()
	for i := 6; i < 12; i++ {
		sleep(ctx, time.Second)
		m := e.say(ctx, ts[i], fmt.Sprintf("s1-%02d-a", i+1), delay)
		fmt.Printf("%s new thread %d: %s\n", at(), i+1, shortDeliveries(m))
		if i >= 8 { // a reply into a thread whose Run is still going: queued, or forwarded to its writer
			m := e.say(ctx, ts[i-2], fmt.Sprintf("s1-%02d-q", i-1), delay)
			fmt.Printf("%s reply into the running thread %d: %s\n", at(), i-1, shortDeliveries(m))
		}
	}
	for i := range 6 { // replies in threads whose Run is in flight or just ended
		sleep(ctx, time.Second)
		m := e.say(ctx, ts[i], fmt.Sprintf("s1-%02d-b", i+1), delay)
		fmt.Printf("%s reply in thread %d: %s\n", at(), i+1, shortDeliveries(m))
	}
	if err := <-rolled; err != nil {
		fmt.Println("rollout:", err)
	}
	fmt.Printf("%s rollout done\n", at())
	for i := 6; i < 9; i++ { // threads whose writer pod is gone
		m := e.say(ctx, ts[i], fmt.Sprintf("s1-%02d-c", i+1), delay)
		fmt.Printf("%s late reply in thread %d: %s\n", at(), i+1, shortDeliveries(m))
		sleep(ctx, 500*time.Millisecond)
	}
	fmt.Printf("%s waiting for the Runs to settle\n", at())
	e.waitQuiet(ctx, ts, 25*time.Second, 90*time.Second)
	fmt.Printf("\n%s results\n", at())
	faults := e.report(ctx, ts)
	return verdict("s1", faults == 0, fmt.Sprintf("%d GOTATO-FAULT", faults))
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
	for i := range 6 {
		ts = append(ts, &thread{chat: fmt.Sprintf("oc_s2_%s_%02d", id, i+1)})
	}
	start := time.Now()
	at := func() string { return fmt.Sprintf("+%02ds", int(time.Since(start).Seconds())) }
	for i, t := range ts {
		m := e.say(ctx, t, fmt.Sprintf("s2-%02d-a", i+1), delay)
		fmt.Printf("%s new thread %d: %s\n", at(), i+1, shortDeliveries(m))
	}
	// Find the writer of the first thread, once its Run holds the Claim.
	var victim string
	var victimInst string
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline) && victim == ""; sleep(ctx, time.Second) {
		if r, err := e.record(ctx, ts[0].session()); err == nil {
			if victimInst = holder(r); victimInst != "" {
				victim = e.podOf(victimInst)
			}
		}
	}
	if victim == "" {
		e.printErrors()
		return verdict("s2", false, "could not find the pod that writes thread 1")
	}
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
		return verdict("s2", false, "kill failed")
	}
	for i, t := range onVictim {
		m := e.say(ctx, t, fmt.Sprintf("s2-r1-%d", i+1), "")
		fmt.Printf("%s immediate reply in %s: %s\n", at(), t.session(), shortDeliveries(m))
	}
	sleep(ctx, time.Until(killAt.Add(5*time.Second)))
	for i, t := range onVictim {
		m := e.say(ctx, t, fmt.Sprintf("s2-mid-%d", i+1), "")
		fmt.Printf("%s reply inside the Liveness TTL in %s: %s\n", at(), t.session(), shortDeliveries(m))
	}
	sleep(ctx, time.Until(killAt.Add(11*time.Second))) // the Liveness TTL is 10 s, renewed every third of it
	for i, t := range onVictim {
		m := e.say(ctx, t, fmt.Sprintf("s2-r2-%d", i+1), "")
		fmt.Printf("%s reply after the Liveness TTL in %s: %s\n", at(), t.session(), shortDeliveries(m))
	}
	fmt.Printf("%s waiting for the Runs to settle\n", at())
	e.waitQuiet(ctx, ts, 25*time.Second, 100*time.Second)

	fmt.Printf("\n%s results\n", at())
	for _, t := range onVictim {
		r, err := e.record(ctx, t.session())
		if err != nil {
			continue
		}
		fmt.Printf("  %s (first thread: %v)\n", t.session(), t == ts[0])
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
	fmt.Println()
	faults := e.report(ctx, ts)
	return verdict("s2", faults == 0, fmt.Sprintf("%d GOTATO-FAULT", faults))
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
