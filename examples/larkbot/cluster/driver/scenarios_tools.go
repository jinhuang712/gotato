package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// The shape of s7: the threads of its phases, the pauses between sends, how long it waits for the MCP server and
// how often it asks, the width of an error text it counts by, the port of fakemcp, and the waits after each part.
const (
	s7WarmThreads  = 9  // W
	s7PhaseThreads = 6  // A, B, R, C, D, G, N
	s7ProbeThreads = 15 // P
	s7QueuedC      = 3  // the C threads that get a message while their Run is going
	s7Gap          = 200 * time.Millisecond
	s7WarmGap      = 100 * time.Millisecond
	s7KillAfter    = 3 * time.Second
	s7UpWait       = 60 * time.Second
	s7ErrTextChars = 110
	mcpStatsPort   = 8090
	s7WarmQuiet    = 4 * time.Second
	s7WarmSettle   = 40 * time.Second
	s7ProbeQuiet   = 6 * time.Second
	s7ProbeSettle  = 90 * time.Second
	s7A2Settle     = 60 * time.Second
	s7BotQuiet     = 10 * time.Second
	s7BotSettle    = 120 * time.Second
	s7FinalQuiet   = 20 * time.Second
	s7FinalSettle  = 120 * time.Second
	scenarioPoll   = 500 * time.Millisecond // how often a scenario looks at what it waits for
)

// The shape of s8: its threads, how long it waits for the load to be a fact, how long after the kill it asks, and
// the waits.
const (
	s8Threads  = 6
	s8CutWait  = 40 * time.Second
	s8AskAfter = 3 * time.Second
	s8Quiet1   = 15 * time.Second
	s8Settle1  = 100 * time.Second
	s8Quiet2   = 10 * time.Second
	s8Settle2  = 60 * time.Second
)

// killWriter finds the pod that writes ts[0], waits until ready holds for every thread that pod writes (a
// Run in flight that has reached the point the scenario wants to cut), and kills the pod.
func (e *env) killWriter(ctx context.Context, st stamper, ts []*thread, ready func(record) bool, limit time.Duration) (inst string, onVictim []*thread, killAt time.Time, err error) {
	var victim string
	inst, victim = e.findWriter(ctx, ts, limit)
	if victim == "" {
		e.printErrors()
		return "", nil, time.Time{}, fmt.Errorf("could not find the pod that writes thread 1")
	}
	pending := 0
	for deadline := time.Now().Add(limit); time.Now().Before(deadline); sleep(ctx, scenarioPoll) {
		onVictim, pending = e.writerThreads(ctx, ts, inst, ready, onVictim)
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

// findWriter waits up to limit for a Run of ts[0] to hold the Claim, and returns the instance and the pod that
// hold it.
func (e *env) findWriter(ctx context.Context, ts []*thread, limit time.Duration) (inst, victim string) {
	for deadline := time.Now().Add(limit); time.Now().Before(deadline) && victim == ""; sleep(ctx, time.Second) {
		if r, rerr := e.record(ctx, ts[0].session()); rerr == nil {
			if inst = holder(r); inst != "" {
				victim = e.podOf(inst)
			}
		}
	}
	return inst, victim
}

// writerThreads returns the threads that inst writes, reusing onVictim, and how many of them ready does not hold
// for yet.
func (e *env) writerThreads(ctx context.Context, ts []*thread, inst string, ready func(record) bool, onVictim []*thread) ([]*thread, int) {
	onVictim = onVictim[:0]
	pending := 0
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
	return onVictim, pending
}

// printTakeover says how the Session changed hands after the kill.
func printTakeover(t *thread, r record, victimInst string, killAt time.Time) {
	if ev, ok := takeoverOf(r, victimInst); ok {
		fmt.Printf("    takeover: claimed by %.8s (why=%s epoch=%d) %s after the kill\n", ev.Instance, ev.Why, ev.Epoch, round(ev.Time.Sub(killAt)))
	} else {
		fmt.Printf("    no takeover claim in the Record of %s\n", t.session())
	}
}

// s7: MCP tools in flight while the MCP server restarts (killed, then rolled gracefully), and while the bot is deployed.
func (e *env) s7(ctx context.Context) bool {
	fmt.Println("== s7: MCP calls in flight during a kill of the MCP server, a rolling deploy of the bot and a graceful roll of the MCP server")
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
	run := &s7Run{e, st, mk, sendAll}
	p, out, err := run.killMCP(ctx)
	if err != nil {
		return fail("s7", out)
	}
	if out, err := run.deployBot(ctx); err != nil {
		return fail("s7", out)
	}
	e.waitQuiet(ctx, all, s7BotQuiet, s7BotSettle)

	// G, H, N, G2: a graceful roll of the MCP server.
	roll, out, err := e.rollMCP(ctx, st, mk, sendAll)
	if err != nil {
		return fail("s7", out)
	}
	fmt.Printf("%s waiting for the Runs to settle\n", st.at())
	e.waitQuiet(ctx, all, s7FinalQuiet, s7FinalSettle)

	fmt.Printf("\n%s results\n", st.at())
	faults := e.report(ctx, all)
	faults += e.reportStoreErrors()
	tl := e.s7Tally(ctx, all)
	e.s7PrintTally(ctx, tl, p, statsBefore)
	ok, errs, stuck, unanswered, emptyErr := tl.ok, tl.errs, tl.stuck, tl.unanswered, tl.emptyErr
	full := errorsByPhase(all, func(t *thread) record { r, _ := e.record(ctx, t.session()); return r })
	faults += reportRoll(roll, ok, errs, full)
	faults += stuck + emptyErr
	if unanswered > 0 {
		faults += unanswered
		fmt.Printf("  %s %d thread(s) end on a user message with no reply\n", fault, unanswered)
	}
	return faultVerdict("s7", faults)
}

// s7Run is one play of s7: the driver, the stamper, and the makers of its threads and of its sends.
type s7Run struct {
	e       *env
	st      stamper
	mk      func(string, int) []*thread
	sendAll func(ts []*thread, phase, suffix, directive string, gap time.Duration)
}

// killMCP plays s7's phases around the SIGKILL of the MCP server (W, A, B, R, P, A2) and returns the P threads;
// out and err say why the kill failed.
func (run *s7Run) killMCP(ctx context.Context) (p []*thread, out string, err error) {
	e, st, mk, sendAll := run.e, run.st, run.mk, run.sendAll
	// W: every pod connects to the MCP server and loads its tools.
	w := mk("W", s7WarmThreads)
	sendAll(w, "W", "a", "[script:mcp=100ms]", s7WarmGap)
	e.waitQuiet(ctx, w, s7WarmQuiet, s7WarmSettle)

	// A: calls of 8 s are in flight when the MCP server pod is killed.
	a := mk("A", s7PhaseThreads)
	sendAll(a, "A", "a", "[script:mcp=8s]", s7Gap)
	sleep(ctx, s7KillAfter)
	// SIGKILL of the container, not `delete --force`: the kubelet sends SIGTERM first even then, and a server that
	// drains on SIGTERM (phase G) would finish its calls and not show what an abrupt end costs.
	fmt.Printf("%s SIGKILL of the fakemcp container (crictl stop -t 0); it restarts in its pod\n", st.at())
	if out, err := e.killContainer(ctx, "fakemcp"); err != nil {
		return nil, out, err
	}
	// B: new threads while the server is down or starting: the Runs resolve the ToolSet and call at once.
	b := mk("B", s7PhaseThreads)
	sendAll(b, "B", "a", "[script:mcp=2s]", s7Gap)
	up := e.waitMCP(ctx, s7UpWait)
	fmt.Printf("%s fakemcp answers again: %s\n", st.at(), up)
	// R: new threads on bot pods that were connected to the old server.
	r := mk("R", s7PhaseThreads)
	sendAll(r, "R", "a", "[script:mcp=2s]", s7Gap)
	// P: one new thread a second, to see when the calls start to work again on the bot pods.
	p = mk("P", s7ProbeThreads)
	for i, t := range p {
		m := e.say(ctx, t, fmt.Sprintf("s7-P-%02d-a", i+1), "[script:mcp=100ms]")
		if m.err != nil {
			fmt.Printf("%s P-a %d: send error %v\n", st.at(), i+1, m.err)
		}
		sleep(ctx, time.Second)
	}
	e.waitQuiet(ctx, append(append(append(append([]*thread{}, a...), b...), r...), p...), s7ProbeQuiet, s7ProbeSettle)
	// A2: a message in each thread whose call was cut, once the server is back.
	sendAll(a, "A2", "b", "[script:mcp=2s]", s7Gap)
	e.waitQuiet(ctx, a, s7ProbeQuiet, s7A2Settle)
	return p, "", nil
}

// deployBot plays s7's phases around a rolling deploy of the bot (C, D, C2); out and err say why the
// rollout could not start.
func (run *s7Run) deployBot(ctx context.Context) (out string, err error) {
	e, st, mk, sendAll := run.e, run.st, run.mk, run.sendAll
	// C: calls of 8 s are in flight when the bot is deployed.
	c := mk("C", s7PhaseThreads)
	sendAll(c, "C", "a", "[script:mcp=8s]", s7Gap)
	fmt.Printf("%s kubectl rollout restart deployment/larkbot\n", st.at())
	if out, err := e.kubectl(ctx, "rollout", "restart", "deployment/larkbot"); err != nil {
		return out, err
	}
	rolled := e.rolloutStatus(ctx, "larkbot")
	for i, t := range c[:s7QueuedC] { // into Runs that are going
		sleep(ctx, time.Second)
		m := e.say(ctx, t, fmt.Sprintf("s7-C-%02d-q", i+1), "[script:mcp=2s]")
		fmt.Printf("%s C-q %d (into the running thread): %s\n", st.at(), i+1, shortDeliveries(m))
	}
	d := mk("D", s7PhaseThreads) // new threads while the pods are replaced
	for i, t := range d {
		sleep(ctx, time.Second)
		m := e.say(ctx, t, fmt.Sprintf("s7-D-%02d-a", i+1), "[script:mcp=4s]")
		fmt.Printf("%s D-a %d: %s\n", st.at(), i+1, shortDeliveries(m))
	}
	if err := <-rolled; err != nil {
		fmt.Println("rollout:", err)
	}
	fmt.Printf("%s rollout done\n", st.at())
	sendAll(c, "C2", "b", "[script:mcp=2s]", s7Gap) // pods that never saw these threads
	return "", nil
}

// s7Tally is what s7 counts over the Records of its threads.
type s7Tally struct {
	ok, errs                    map[string]int
	stuck, unanswered, emptyErr int
	errTexts                    map[string]int
	resolveFail                 map[string]int // extension_failed events of the ToolSet, by phase and message
	failedRuns                  int
}

// s7Tally reads the Record of every thread and counts its tool calls, errors and Runs.
func (e *env) s7Tally(ctx context.Context, all []*thread) s7Tally {
	tl := s7Tally{ok: map[string]int{}, errs: map[string]int{}, errTexts: map[string]int{}, resolveFail: map[string]int{}}
	for _, t := range all {
		r, err := e.record(ctx, t.session())
		if err != nil {
			continue
		}
		tl.add(t, r)
	}
	return tl
}

// add counts one thread's Record.
func (tl *s7Tally) add(t *thread, r record) {
	a := checkMCP(r)
	a.tally(tl.ok, tl.errs)
	tl.addTexts(a.Texts)
	for _, ev := range r.Events {
		if ev.Kind == "extension_failed" && strings.HasPrefix(ev.Stage, "toolset:") {
			tl.resolveFail[ev.Stage+" "+ev.Code]++
		}
	}
	for _, run := range r.Runs {
		if run.Status != "completed" {
			tl.failedRuns++
		}
	}
	if inFlight(r) > 0 {
		tl.stuck++
		fmt.Printf("  %s %s: a Run has not ended\n", fault, t.session())
	}
	if r.unanswered() {
		tl.unanswered++
	}
}

// addTexts counts the texts of the tool errors the model reported, and those with no text.
func (tl *s7Tally) addTexts(texts []string) {
	for _, text := range texts {
		tl.errTexts[trunc(text, s7ErrTextChars)]++
		if strings.TrimSpace(strings.TrimPrefix(text, ansMCPError)) == "" {
			tl.emptyErr++
		}
	}
}

// s7PrintTally prints what s7 counted, phase P one thread a second, and the bot log's toolset errors.
func (e *env) s7PrintTally(ctx context.Context, tl s7Tally, p []*thread, statsBefore string) {
	ok, errs, errTexts, resolveFail := tl.ok, tl.errs, tl.errTexts, tl.resolveFail
	stuck, unanswered, emptyErr, failedRuns := tl.stuck, tl.unanswered, tl.emptyErr, tl.failedRuns
	phases := []string{"W", "A", "B", "R", "P", "A2", "C", "D", "C2", "G", "H", "N", "G2"}
	okN, errN := 0, 0
	for _, p := range phases {
		okN += ok[p]
		errN += errs[p]
	}
	fmt.Printf("  tool calls the model reported, by phase (W warm-up; A calls in flight when the server is killed; B sent while it restarts; R after it answers again; P one a second after that; A2 a second message in the A threads; C and D during the bot deploy; C2 after it; G in flight when the server is rolled gracefully, H one a second during the roll, N new threads after it, G2 a second message in the G threads): %s\n", phaseCounts(ok, errs, phases))
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
}

// waitMCP waits until fakemcp answers /stats through its Service, and returns what it says.
func (e *env) waitMCP(ctx context.Context, limit time.Duration) string {
	var s string
	for deadline := time.Now().Add(limit); time.Now().Before(deadline) && ctx.Err() == nil; sleep(ctx, scenarioPoll) {
		if s = e.mcpStats(ctx); strings.HasPrefix(s, "calls") {
			break
		}
	}
	return s
}

// mcpStats reads fakemcp's counters through the API server's service proxy.
func (e *env) mcpStats(ctx context.Context) string {
	var s struct{ Started, Finished, Canceled int }
	if err := e.do(ctx, "GET", e.svc("fakemcp", mcpStatsPort)+"/stats", nil, &s); err != nil {
		return "stats: " + err.Error()
	}
	return fmt.Sprintf("calls since this pod started: %d started, %d finished, %d canceled by the caller", s.Started, s.Finished, s.Canceled)
}

// s8: tool search. The model loads one tool of the catalog; the writer is killed after the load is a fact of
// the Record; the next message goes to another pod, which says whether the tool is still loaded.
func (e *env) s8(ctx context.Context) bool {
	fmt.Println("== s8: is a tool loaded with search_tools still loaded after the Session changes hands?")
	const delay = "[quirk:delay=8s]"
	st := newStamper()
	ts := chats("s8", s8Threads)
	for i, t := range ts {
		m := e.say(ctx, t, fmt.Sprintf("s8-%02d-a", i+1), "[script:search] "+delay)
		fmt.Printf("%s new thread %d: %s\n", st.at(), i+1, shortDeliveries(m))
	}
	// search_tools runs after the first model call (8 s); the Run goes on to call the tool after the second.
	loadedInFlight := func(r record) bool { return inFlight(r) > 0 && r.hasEvent("tool_loaded", catalogTool) }
	victimInst, onVictim, killAt, err := e.killWriter(ctx, st, ts, loadedInFlight, s8CutWait)
	if err != nil {
		return fail("s8", err.Error())
	}
	onV := e.s8AskLoaded(ctx, st, ts, onVictim, killAt)

	fmt.Printf("\n%s results\n", st.at())
	faults, yes, no := e.s8Results(ctx, ts, onV, victimInst, killAt)
	fmt.Println()
	faults += e.report(ctx, ts)
	faults += e.reportStoreErrors()
	fmt.Printf("  tool still loaded on the next message: victims %d of %d, survivors %d of %d\n", yes["victim"], yes["victim"]+no["victim"], yes["survivor"], yes["survivor"]+no["survivor"])
	return faultVerdict("s8", faults)
}

// s8AskLoaded asks each thread whether the tool is still loaded: first the threads of the killed writer, inside
// the Liveness TTL, then the others once the Runs settle. It returns the threads of the killed writer.
func (e *env) s8AskLoaded(ctx context.Context, st stamper, ts, onVictim []*thread, killAt time.Time) map[*thread]bool {
	onV := map[*thread]bool{}
	sleep(ctx, time.Until(killAt.Add(s8AskAfter))) // inside the Liveness TTL
	for _, t := range onVictim {
		onV[t] = true
		m := e.say(ctx, t, fmt.Sprintf("s8-%02d-b", t.n), "[script:loaded]")
		fmt.Printf("%s loaded? after the kill in %s: %s\n", st.at(), t.session(), shortDeliveries(m))
	}
	fmt.Printf("%s waiting for the Runs to settle\n", st.at())
	e.waitQuiet(ctx, ts, s8Quiet1, s8Settle1)
	for _, t := range ts {
		if !onV[t] {
			m := e.say(ctx, t, fmt.Sprintf("s8-%02d-b", t.n), "[script:loaded]")
			fmt.Printf("%s loaded? in %s (writer alive): %s\n", st.at(), t.session(), shortDeliveries(m))
		}
	}
	e.waitQuiet(ctx, ts, s8Quiet2, s8Settle2)
	return onV
}

// s8Tally is what s8 counts over its threads: the GOTATO-FAULTs, and the answers that the tool is loaded or not, by
// role.
type s8Tally struct {
	faults  int
	yes, no map[string]int
}

// s8Results checks each thread's Record and the model's answer to the loaded? message.
func (e *env) s8Results(ctx context.Context, ts []*thread, onV map[*thread]bool, victimInst string, killAt time.Time) (int, map[string]int, map[string]int) {
	tl := s8Tally{yes: map[string]int{}, no: map[string]int{}}
	for _, t := range ts {
		r, err := e.record(ctx, t.session())
		if err != nil {
			tl.faults++
			fmt.Printf("  %s %s: cannot read the Record: %v\n", fault, t.session(), err)
			continue
		}
		tl.add(t, r, onV, victimInst, killAt)
	}
	return tl.faults, tl.yes, tl.no
}

// add checks one thread's Record: the takeover of a victim, and the model's answer to the loaded? message.
func (tl *s8Tally) add(t *thread, r record, onV map[*thread]bool, victimInst string, killAt time.Time) {
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
		return // the loaded? message is not in the Transcript: report below classifies it
	}
	if len(ans) != 1 {
		tl.faults++
		fmt.Printf("  %s %s: %d answers to the loaded? message, want 1\n", fault, t.session(), len(ans))
		return
	}
	if ans[0] {
		tl.yes[role]++
	} else {
		tl.no[role]++
	}
	if !ans[0] && len(loadedTools(r)) > 0 {
		tl.faults++
		fmt.Printf("  %s %s: the Record says %v is loaded, and the model on the next message does not see it loaded\n", fault, t.session(), loadedTools(r))
	}
}
