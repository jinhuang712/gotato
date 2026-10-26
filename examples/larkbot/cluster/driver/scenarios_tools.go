package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jinhuang712/gotato/examples/larkbot/internal/lookup"
)

// killWriter finds the pod that writes ts[0], waits until ready holds for every thread that pod writes (a
// Run in flight that has reached the point the scenario wants to cut), and kills the pod.
func (e *env) killWriter(ctx context.Context, st stamper, ts []*thread, ready func(record) bool, limit time.Duration) (inst string, onVictim []*thread, killAt time.Time, err error) {
	var victim string
	for deadline := time.Now().Add(limit); time.Now().Before(deadline) && victim == ""; sleep(ctx, time.Second) {
		if r, rerr := e.record(ctx, ts[0].session()); rerr == nil {
			if inst = holder(r); inst != "" {
				victim = e.podOf(inst)
			}
		}
	}
	if victim == "" {
		e.printErrors()
		return "", nil, time.Time{}, fmt.Errorf("could not find the pod that writes thread 1")
	}
	pending := 0
	for deadline := time.Now().Add(limit); time.Now().Before(deadline); sleep(ctx, 500*time.Millisecond) {
		onVictim = onVictim[:0]
		pending = 0
		for _, t := range ts {
			r, rerr := e.record(ctx, t.session())
			if rerr != nil || holder(r) != inst {
				continue
			}
			onVictim = append(onVictim, t)
			if !ready(r) {
				pending++
			}
		}
		if pending == 0 && len(onVictim) > 0 {
			break
		}
	}
	if pending > 0 || len(onVictim) == 0 {
		return "", nil, time.Time{}, fmt.Errorf("%d of the %d threads the writer holds never reached the point to cut within %s", pending, len(onVictim), limit)
	}
	fmt.Printf("%s writer of thread 1 is pod %s (instance %.8s); it writes %d of the %d threads, each at the point to cut\n", st.at(), victim, inst, len(onVictim), len(ts))
	e.victimPod = victim
	e.victimIP, _ = e.kubectl(ctx, "get", "pod", victim, "-o", "jsonpath={.status.podIP}")
	killAt = time.Now()
	out, kerr := e.kill(ctx, victim)
	fmt.Printf("%s killed %s (%s): %s\n", st.at(), victim, e.killMode, out)
	if kerr != nil {
		return inst, onVictim, killAt, fmt.Errorf("kill failed")
	}
	return inst, onVictim, killAt, nil
}

// printTakeover says how the Session changed hands after the kill.
func printTakeover(t *thread, r record, victimInst string, killAt time.Time) {
	if ev, ok := takeoverOf(r, victimInst); ok {
		fmt.Printf("    takeover: claimed by %.8s (why=%s epoch=%d) %s after the kill\n", ev.Instance, ev.Why, ev.Epoch, round(ev.Time.Sub(killAt)))
	} else {
		fmt.Printf("    no takeover claim in the Record of %s\n", t.session())
	}
}

// s6: the bash tool. A Run calls a tool whose result is spilled by the core; the writer is killed in that Run;
// the next message goes to another pod, which has the model read the spilled result with bash.
func (e *env) s6(ctx context.Context) bool {
	fmt.Println("== s6: bash reads a spilled result from the pod that took the Session over")
	const delay = "[quirk:delay=10s]"
	want := lookup.Expected()
	fmt.Printf("expected answer of every lookup: %q\n", want)
	st := newStamper()
	ts := chats("s6", 6)
	for i, t := range ts {
		m := e.say(ctx, t, fmt.Sprintf("s6-%02d-a", i+1), "[script:export] "+delay)
		fmt.Printf("%s new thread %d: %s\n", st.at(), i+1, shortDeliveries(m))
	}
	// The export is called after the first model call (10 s) and its result is committed at once; the Run
	// would answer after the second (10 s more). Cut it in between.
	inFlightSpilled := func(r record) bool {
		return inFlight(r) > 0 && r.hasEvent("tool_result_spilled", exportTool) && r.hasEvent("tool_result_committed", "")
	}
	victimInst, onVictim, killAt, err := e.killWriter(ctx, st, ts, inFlightSpilled, 40*time.Second)
	if err != nil {
		return verdict("s6", false, err.Error())
	}
	// Inside the Liveness TTL (10 s), as s2's mid reply: the pod that gets the message cannot take the Session
	// yet, so the bot retries until the writer's Liveness row has expired. A message sent at the very moment of
	// the kill can still reach the dying pod's memory (s2 shows that, KNOWN-BOT-GAP).
	sleep(ctx, time.Until(killAt.Add(3*time.Second)))
	for _, t := range onVictim {
		m := e.say(ctx, t, fmt.Sprintf("s6-%02d-b", t.n), "[script:lookup]")
		fmt.Printf("%s lookup after the kill in %s: %s\n", st.at(), t.session(), shortDeliveries(m))
	}
	fmt.Printf("%s waiting for the Runs to settle\n", st.at())
	e.waitQuiet(ctx, ts, 15*time.Second, 100*time.Second)
	onV := map[*thread]bool{}
	for _, t := range onVictim {
		onV[t] = true
	}
	for _, t := range ts { // the pods that were not killed: the same lookup, as the control
		if !onV[t] {
			m := e.say(ctx, t, fmt.Sprintf("s6-%02d-b", t.n), "[script:lookup]")
			fmt.Printf("%s lookup in %s (writer alive): %s\n", st.at(), t.session(), shortDeliveries(m))
		}
	}
	e.waitQuiet(ctx, ts, 10*time.Second, 60*time.Second)

	fmt.Printf("\n%s results\n", st.at())
	faults := 0
	var spilled, bashOK, bashFailed, answers, correct, victimCorrect, survivorCorrect, dups, noTakeover int
	for _, t := range ts {
		r, err := e.record(ctx, t.session())
		if err != nil {
			faults++
			fmt.Printf("  %s %s: cannot read the Record: %v\n", fault, t.session(), err)
			continue
		}
		c := checkLookup(r, want)
		bIn := r.count(t.msgs[len(t.msgs)-1].tag) == 1 // a message that is not in the Transcript is classified by report below
		role := "survivor"
		if onV[t] {
			role = "victim"
			if _, ok := takeoverOf(r, victimInst); !ok {
				noTakeover++
			}
		}
		fmt.Printf("  %s (%s): spilled results %d (%d bytes), bash ok %d failed %d, lookup answers %d correct %d, Runs %s\n",
			t.session(), role, c.Spilled, c.SpilledBytes, c.BashOK, c.BashFailed, c.Answers, c.Correct, runStatuses(r))
		if onV[t] {
			printTakeover(t, r, victimInst, killAt)
		}
		spilled += c.Spilled
		bashOK += c.BashOK
		bashFailed += c.BashFailed
		answers += c.Answers
		correct += c.Correct
		dups += c.DupCalls
		if onV[t] {
			victimCorrect += c.Correct
		} else {
			survivorCorrect += c.Correct
		}
		if c.Spilled == 0 {
			faults++
			fmt.Printf("  %s %s: the export result is not spilled in the Record\n", fault, t.session())
		}
		if bIn && (c.Correct != 1 || c.BashFailed > 0 || c.DupCalls > 0) {
			faults++
			fmt.Printf("  %s %s: lookup correct %d (want 1), bash failed %d, duplicate call IDs %d, wrong answers %q\n", fault, t.session(), c.Correct, c.BashFailed, c.DupCalls, c.Wrong)
		}
	}
	fmt.Println()
	faults += e.report(ctx, ts)
	for _, s := range storeErrors(e.readLogs()) {
		faults++
		fmt.Printf("  %s store error in a bot log: %s\n", fault, s)
	}
	fmt.Printf("  threads %d (the killed pod wrote %d, no takeover claim in %d), spilled export results %d\n", len(ts), len(onVictim), noTakeover, spilled)
	fmt.Printf("  bash results: ok %d, failed %d; lookup answers %d, equal to the expected one %d (victims %d, survivors %d); duplicate call IDs %d\n",
		bashOK, bashFailed, answers, correct, victimCorrect, survivorCorrect, dups)
	return verdict("s6", faults == 0, fmt.Sprintf("%d GOTATO-FAULT", faults))
}

// s7: MCP tools in flight while the MCP server restarts, and while the bot is deployed.
func (e *env) s7(ctx context.Context) bool {
	fmt.Println("== s7: MCP calls in flight during a restart of the MCP server and a rolling deploy of the bot")
	st := newStamper()
	statsBefore := e.mcpStats(ctx)
	fmt.Printf("%s fakemcp %s\n", st.at(), statsBefore)
	var all []*thread
	mk := func(phase string, n int) []*thread {
		ts := chats("s7"+phase, n)
		all = append(all, ts...)
		return ts
	}
	sendAll := func(ts []*thread, phase, suffix, directive string, gap time.Duration) {
		for i, t := range ts {
			m := e.say(ctx, t, fmt.Sprintf("s7-%s-%02d-%s", phase, i+1, suffix), directive)
			fmt.Printf("%s %s %d: %s\n", st.at(), phase+"-"+suffix, i+1, shortDeliveries(m))
			sleep(ctx, gap)
		}
	}
	// W: every pod connects to the MCP server and loads its tools.
	w := mk("W", 9)
	sendAll(w, "W", "a", "[script:mcp=100ms]", 100*time.Millisecond)
	e.waitQuiet(ctx, w, 4*time.Second, 40*time.Second)

	// A: calls of 8 s are in flight when the MCP server pod is killed.
	a := mk("A", 6)
	sendAll(a, "A", "a", "[script:mcp=8s]", 200*time.Millisecond)
	sleep(ctx, 3*time.Second)
	fmt.Printf("%s kubectl delete pod -l app=fakemcp --grace-period=0 --force\n", st.at())
	out, err := e.kubectl(ctx, "delete", "pod", "-l", "app=fakemcp", "--grace-period=0", "--force")
	if err != nil {
		return verdict("s7", false, out)
	}
	// B: new threads while the server is down or starting: the Runs resolve the ToolSet and call at once.
	b := mk("B", 6)
	sendAll(b, "B", "a", "[script:mcp=2s]", 200*time.Millisecond)
	up := e.waitMCP(ctx, 60*time.Second)
	fmt.Printf("%s fakemcp answers again: %s\n", st.at(), up)
	// R: new threads on bot pods that were connected to the old server.
	r := mk("R", 6)
	sendAll(r, "R", "a", "[script:mcp=2s]", 200*time.Millisecond)
	// P: one new thread a second, to see when the calls start to work again on the bot pods.
	p := mk("P", 15)
	for i, t := range p {
		m := e.say(ctx, t, fmt.Sprintf("s7-P-%02d-a", i+1), "[script:mcp=100ms]")
		if m.err != nil {
			fmt.Printf("%s P-a %d: send error %v\n", st.at(), i+1, m.err)
		}
		sleep(ctx, time.Second)
	}
	e.waitQuiet(ctx, append(append(append(append([]*thread{}, a...), b...), r...), p...), 6*time.Second, 90*time.Second)
	// A2: a message in each thread whose call was cut, once the server is back.
	sendAll(a, "A2", "b", "[script:mcp=2s]", 200*time.Millisecond)
	e.waitQuiet(ctx, a, 6*time.Second, 60*time.Second)

	// C: calls of 8 s are in flight when the bot is deployed.
	c := mk("C", 6)
	sendAll(c, "C", "a", "[script:mcp=8s]", 200*time.Millisecond)
	fmt.Printf("%s kubectl rollout restart deployment/larkbot\n", st.at())
	if out, err := e.kubectl(ctx, "rollout", "restart", "deployment/larkbot"); err != nil {
		return verdict("s7", false, out)
	}
	rolled := make(chan error, 1)
	go func() {
		out, err := e.kubectl(ctx, "rollout", "status", "deployment/larkbot", "--timeout=170s")
		if err != nil {
			err = fmt.Errorf("%w: %s", err, out)
		}
		rolled <- err
	}()
	for i, t := range c[:3] { // into Runs that are going
		sleep(ctx, time.Second)
		m := e.say(ctx, t, fmt.Sprintf("s7-C-%02d-q", i+1), "[script:mcp=2s]")
		fmt.Printf("%s C-q %d (into the running thread): %s\n", st.at(), i+1, shortDeliveries(m))
	}
	d := mk("D", 6) // new threads while the pods are replaced
	for i, t := range d {
		sleep(ctx, time.Second)
		m := e.say(ctx, t, fmt.Sprintf("s7-D-%02d-a", i+1), "[script:mcp=4s]")
		fmt.Printf("%s D-a %d: %s\n", st.at(), i+1, shortDeliveries(m))
	}
	if err := <-rolled; err != nil {
		fmt.Println("rollout:", err)
	}
	fmt.Printf("%s rollout done\n", st.at())
	sendAll(c, "C2", "b", "[script:mcp=2s]", 200*time.Millisecond) // pods that never saw these threads
	fmt.Printf("%s waiting for the Runs to settle\n", st.at())
	e.waitQuiet(ctx, all, 20*time.Second, 120*time.Second)

	fmt.Printf("\n%s results\n", st.at())
	faults := e.report(ctx, all)
	for _, s := range storeErrors(e.readLogs()) {
		faults++
		fmt.Printf("  %s store error in a bot log: %s\n", fault, s)
	}
	ok, errs := map[string]int{}, map[string]int{}
	stuck, unanswered, emptyErr := 0, 0, 0
	errTexts := map[string]int{}
	resolveFail := map[string]int{} // extension_failed events of the ToolSet, by phase and message
	failedRuns := 0
	for _, t := range all {
		r, err := e.record(ctx, t.session())
		if err != nil {
			continue
		}
		a := checkMCP(r)
		a.tally(ok, errs)
		for _, text := range a.Texts {
			errTexts[trunc(text, 110)]++
			if strings.TrimSpace(strings.TrimPrefix(text, ansMCPError)) == "" {
				emptyErr++
			}
		}
		for _, ev := range r.Events {
			if ev.Kind == "extension_failed" && strings.HasPrefix(ev.Stage, "toolset:") {
				resolveFail[ev.Stage+" "+ev.Code]++
			}
		}
		for _, run := range r.Runs {
			if run.Status != "completed" {
				failedRuns++
			}
		}
		if inFlight(r) > 0 {
			stuck++
			fmt.Printf("  %s %s: a Run has not ended\n", fault, t.session())
		}
		if r.unanswered() {
			unanswered++
		}
	}
	phases := []string{"W", "A", "B", "R", "P", "A2", "C", "D", "C2"}
	okN, errN := 0, 0
	for _, p := range phases {
		okN += ok[p]
		errN += errs[p]
	}
	fmt.Printf("  tool calls the model reported, by phase (W warm-up; A calls in flight when the server is killed; B sent while it restarts; R after it answers again; P one a second after that; A2 a second message in the A threads; C and D during the bot deploy; C2 after it): %s\n", phaseCounts(ok, errs, phases))
	fmt.Printf("  total: %d succeeded, %d came back as a tool error\n", okN, errN)
	for _, k := range sortedKeys(errTexts) {
		fmt.Printf("    tool error x%d: %s\n", errTexts[k], k)
	}
	for _, k := range sortedKeys(resolveFail) {
		fmt.Printf("    ToolSet failed to resolve (extension_failed) x%d: %s\n", resolveFail[k], k)
	}
	fmt.Printf("  phase P, one thread a second from the moment fakemcp answers (o ok, E tool error): %s\n", sequence(p, func(t *thread) mcpAnswers {
		r, _ := e.record(ctx, t.session())
		return checkMCP(r)
	}))
	causes := toolsetErrors(e.readLogs())
	for _, k := range sortedKeys(causes) {
		fmt.Printf("    bot log \"toolset unavailable\" x%d: %s\n", causes[k], trunc(k, 200))
	}
	fmt.Printf("  Runs that did not complete: %d\n", failedRuns)
	fmt.Printf("  Runs not ended %d, threads that end on a user message %d, tool errors with no text %d\n", stuck, unanswered, emptyErr)
	fmt.Printf("  fakemcp now: %s (before: %s)\n", e.mcpStats(ctx), statsBefore)
	faults += stuck + emptyErr
	if unanswered > 0 {
		faults += unanswered
		fmt.Printf("  %s %d thread(s) end on a user message with no reply\n", fault, unanswered)
	}
	return verdict("s7", faults == 0, fmt.Sprintf("%d GOTATO-FAULT", faults))
}

// waitMCP waits until fakemcp answers /stats through its Service, and returns what it says.
func (e *env) waitMCP(ctx context.Context, limit time.Duration) string {
	var s string
	for deadline := time.Now().Add(limit); time.Now().Before(deadline) && ctx.Err() == nil; sleep(ctx, 500*time.Millisecond) {
		if s = e.mcpStats(ctx); strings.HasPrefix(s, "calls") {
			break
		}
	}
	return s
}

// mcpStats reads fakemcp's counters through the API server's service proxy.
func (e *env) mcpStats(ctx context.Context) string {
	var s struct{ Started, Finished, Cancelled int }
	if err := e.do(ctx, "GET", e.svc("fakemcp", 8090)+"/stats", nil, &s); err != nil {
		return "stats: " + err.Error()
	}
	return fmt.Sprintf("calls since this pod started: %d started, %d finished, %d cancelled by the caller", s.Started, s.Finished, s.Cancelled)
}

// s8: tool search. The model loads one tool of the catalog; the writer is killed after the load is a fact of
// the Record; the next message goes to another pod, which says whether the tool is still loaded.
func (e *env) s8(ctx context.Context) bool {
	fmt.Println("== s8: is a tool loaded with search_tools still loaded after the Session changes hands?")
	const delay = "[quirk:delay=8s]"
	st := newStamper()
	ts := chats("s8", 6)
	for i, t := range ts {
		m := e.say(ctx, t, fmt.Sprintf("s8-%02d-a", i+1), "[script:search] "+delay)
		fmt.Printf("%s new thread %d: %s\n", st.at(), i+1, shortDeliveries(m))
	}
	// search_tools runs after the first model call (8 s); the Run goes on to call the tool after the second.
	loadedInFlight := func(r record) bool { return inFlight(r) > 0 && r.hasEvent("tool_loaded", catalogTool) }
	victimInst, onVictim, killAt, err := e.killWriter(ctx, st, ts, loadedInFlight, 40*time.Second)
	if err != nil {
		return verdict("s8", false, err.Error())
	}
	onV := map[*thread]bool{}
	sleep(ctx, time.Until(killAt.Add(3*time.Second))) // inside the Liveness TTL, as in s6
	for _, t := range onVictim {
		onV[t] = true
		m := e.say(ctx, t, fmt.Sprintf("s8-%02d-b", t.n), "[script:loaded]")
		fmt.Printf("%s loaded? after the kill in %s: %s\n", st.at(), t.session(), shortDeliveries(m))
	}
	fmt.Printf("%s waiting for the Runs to settle\n", st.at())
	e.waitQuiet(ctx, ts, 15*time.Second, 100*time.Second)
	for _, t := range ts {
		if !onV[t] {
			m := e.say(ctx, t, fmt.Sprintf("s8-%02d-b", t.n), "[script:loaded]")
			fmt.Printf("%s loaded? in %s (writer alive): %s\n", st.at(), t.session(), shortDeliveries(m))
		}
	}
	e.waitQuiet(ctx, ts, 10*time.Second, 60*time.Second)

	fmt.Printf("\n%s results\n", st.at())
	faults := 0
	yes := map[string]int{}
	no := map[string]int{}
	for _, t := range ts {
		r, err := e.record(ctx, t.session())
		if err != nil {
			faults++
			fmt.Printf("  %s %s: cannot read the Record: %v\n", fault, t.session(), err)
			continue
		}
		role := "survivor"
		if onV[t] {
			role = "victim"
		}
		ans := loadedAnswers(r)
		fmt.Printf("  %s (%s): tool_loaded in the Record %v, the model's answer on the next message %v, Runs %s\n", t.session(), role, loadedTools(r), ans, runStatuses(r))
		if onV[t] {
			printTakeover(t, r, victimInst, killAt)
		}
		if r.count(t.msgs[len(t.msgs)-1].tag) != 1 {
			continue // the loaded? message is not in the Transcript: report below classifies it
		}
		if len(ans) != 1 {
			faults++
			fmt.Printf("  %s %s: %d answers to the loaded? message, want 1\n", fault, t.session(), len(ans))
			continue
		}
		if ans[0] {
			yes[role]++
		} else {
			no[role]++
		}
		if !ans[0] && len(loadedTools(r)) > 0 {
			faults++
			fmt.Printf("  %s %s: the Record says %v is loaded, and the model on the next message does not see it loaded\n", fault, t.session(), loadedTools(r))
		}
	}
	fmt.Println()
	faults += e.report(ctx, ts)
	for _, s := range storeErrors(e.readLogs()) {
		faults++
		fmt.Printf("  %s store error in a bot log: %s\n", fault, s)
	}
	fmt.Printf("  tool still loaded on the next message: victims %d of %d, survivors %d of %d\n", yes["victim"], yes["victim"]+no["victim"], yes["survivor"], yes["survivor"]+no["survivor"])
	return verdict("s8", faults == 0, fmt.Sprintf("%d GOTATO-FAULT", faults))
}

// runStatuses lists the status of each Run of the Record, such as "failed, completed".
func runStatuses(r record) string {
	var out []string
	for _, run := range r.Runs {
		out = append(out, run.Status)
	}
	return strings.Join(out, ", ")
}
