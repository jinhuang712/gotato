package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// s10: a graceful roll of the MCP server. The same calls as s7's phase A (8 s at the server) are in flight when
// the server pod is replaced: first the abrupt way (force delete, no SIGTERM: what s7 does), then the way a
// deploy does it (rollout restart: SIGTERM, fakemcp drains, the new pod takes over). Calls sent during and after
// the roll are counted, and the model's reports are read for "session not found".
func (e *env) s10(ctx context.Context) bool {
	fmt.Println("== s10: graceful roll of the MCP server with calls in flight, next to an abrupt kill")
	st := newStamper()
	fmt.Printf("%s fakemcp %s\n", st.at(), e.mcpStats(ctx))
	var all []*thread
	mk := func(phase string, n int) []*thread {
		ts := chats("s10"+phase, n)
		all = append(all, ts...)
		return ts
	}
	sendAll := func(ts []*thread, phase, suffix, directive string, gap time.Duration) {
		for i, t := range ts {
			m := e.say(ctx, t, fmt.Sprintf("s10-%s-%02d-%s", phase, i+1, suffix), directive)
			if m.err != nil {
				fmt.Printf("%s %s-%s %d: send error %v\n", st.at(), phase, suffix, i+1, m.err)
			}
			sleep(ctx, gap)
		}
	}
	w := mk("W", 9) // every bot pod connects
	sendAll(w, "W", "a", "[script:mcp=100ms]", 100*time.Millisecond)
	e.waitQuiet(ctx, w, 4*time.Second, 40*time.Second)

	// Abrupt, as s7: A is in flight, B is sent while the server is down, R after it answers.
	a := mk("A", 6)
	sendAll(a, "A", "a", "[script:mcp=8s]", 200*time.Millisecond)
	sleep(ctx, 3*time.Second)
	// SIGKILL of the container, not `delete --force`: the kubelet sends SIGTERM first even then, and a server that
	// drains on SIGTERM would finish its calls and not show what an abrupt end costs.
	fmt.Printf("%s SIGKILL of the fakemcp container (crictl stop -t 0); it restarts in its pod\n", st.at())
	if out, err := e.killContainer(ctx, "fakemcp"); err != nil {
		return verdict("s10", false, out)
	}
	b := mk("B", 6)
	sendAll(b, "B", "a", "[script:mcp=2s]", 200*time.Millisecond)
	fmt.Printf("%s fakemcp answers again: %s\n", st.at(), e.waitMCP(ctx, 60*time.Second))
	r := mk("R", 6)
	sendAll(r, "R", "a", "[script:mcp=2s]", 200*time.Millisecond)
	e.waitQuiet(ctx, append(append(append([]*thread{}, a...), b...), r...), 6*time.Second, 90*time.Second)
	e.waitReadyPods(ctx, "fakemcp", 60*time.Second)

	// Graceful: G is in flight when `rollout restart` starts. H is one new thread a second while it goes on.
	oldPod, _ := e.kubectl(ctx, "get", "pods", "-l", "app=fakemcp", "-o", "jsonpath={.items[0].metadata.name}")
	logPath := filepath.Join(e.logDir, "fakemcp-"+oldPod+".log")
	stopFollow := e.followFile(ctx, oldPod, logPath)
	g := mk("G", 6)
	sendAll(g, "G", "a", "[script:mcp=8s]", 200*time.Millisecond)
	sleep(ctx, 2*time.Second)
	fmt.Printf("%s graceful: kubectl rollout restart deployment/fakemcp (old pod %s)\n", st.at(), oldPod)
	rollAt := time.Now()
	if out, err := e.kubectl(ctx, "rollout", "restart", "deployment/fakemcp"); err != nil {
		stopFollow()
		return verdict("s10", false, out)
	}
	// The roll lasts until the old pod is gone: `rollout status` returns once the new pod is ready, while the
	// old one is still draining its calls.
	rolled := make(chan error, 1)
	go func() {
		out, err := e.kubectl(ctx, "rollout", "status", "deployment/fakemcp", "--timeout=120s")
		if err == nil {
			out, err = e.kubectl(ctx, "wait", "--for=delete", "pod/"+oldPod, "--timeout=60s")
		}
		if err != nil {
			err = fmt.Errorf("%w: %s", err, out)
		}
		rolled <- err
	}()
	var h []*thread
	var rollErr error
	for done := false; !done && len(h) < 60; {
		select {
		case rollErr = <-rolled:
			done = true
		default:
			t := mk("H", 1)[0]
			t.n = len(h) + 1
			h = append(h, t)
			m := e.say(ctx, t, fmt.Sprintf("s10-H-%02d-a", t.n), "[script:mcp=2s]")
			if m.err != nil {
				fmt.Printf("%s H-a %d: send error %v\n", st.at(), t.n, m.err)
			}
			sleep(ctx, time.Second)
		}
	}
	if rollErr != nil {
		fmt.Println("rollout:", rollErr)
	}
	fmt.Printf("%s rollout done and the old pod gone after %s; %d threads sent during it\n", st.at(), round(time.Since(rollAt)), len(h))
	e.waitQuiet(ctx, g, 4*time.Second, 60*time.Second)
	stopFollow()
	n := mk("N", 6) // new threads after the roll
	sendAll(n, "N", "a", "[script:mcp=2s]", 200*time.Millisecond)
	sendAll(g, "G2", "b", "[script:mcp=2s]", 200*time.Millisecond) // a second message in the threads that were in flight
	fmt.Printf("%s waiting for the Runs to settle\n", st.at())
	e.waitQuiet(ctx, all, 10*time.Second, 120*time.Second)

	fmt.Printf("\n%s results\n", st.at())
	faults := e.report(ctx, all)
	for _, s := range storeErrors(e.readLogs()) {
		faults++
		fmt.Printf("  %s store error in a bot log: %s\n", fault, s)
	}
	ok, errs := map[string]int{}, map[string]int{}
	stuck, unanswered, emptyErr, sessionNotFound, transport := 0, 0, 0, 0, 0
	for _, t := range all {
		rec, err := e.record(ctx, t.session())
		if err != nil {
			continue
		}
		a := checkMCP(rec)
		a.tally(ok, errs)
		for _, text := range a.Texts {
			if strings.TrimSpace(strings.TrimPrefix(text, ansMCPError)) == "" {
				emptyErr++
			}
			if strings.Contains(text, "session not found") {
				sessionNotFound++
			}
			if strings.Contains(text, "rejected by transport") {
				transport++
			}
		}
		if inFlight(rec) > 0 {
			stuck++
			fmt.Printf("  %s %s: a Run has not ended\n", fault, t.session())
		}
		if rec.unanswered() {
			unanswered++
		}
	}
	errTexts, full := errorsByPhase(all, func(t *thread) record { rec, _ := e.record(ctx, t.session()); return rec })
	phases := []string{"W", "A", "B", "R", "G", "H", "N", "G2"}
	fmt.Printf("  tool calls the model reported, by phase (W warm-up; abrupt kill: A in flight, B sent while it restarts, R after it answers; graceful roll: G in flight, H one a second during the roll, N new threads after, G2 a second message in the G threads): %s\n", phaseCounts(ok, errs, phases))
	for _, ph := range phases {
		for _, k := range sortedKeys(errTexts[ph]) {
			fmt.Printf("    %s tool error x%d: %s\n", ph, errTexts[ph][k], k)
		}
	}
	causes := toolsetErrors(e.readLogs())
	for _, k := range sortedKeys(causes) {
		fmt.Printf("    bot log \"toolset unavailable\" x%d: %s\n", causes[k], trunc(k, 200))
	}
	fmt.Printf("  SIGKILL (A, B, R): %d ok / %d error; graceful roll (G, H, N, G2): %d ok / %d error\n",
		ok["A"]+ok["B"]+ok["R"], errs["A"]+errs["B"]+errs["R"], ok["G"]+ok["H"]+ok["N"]+ok["G2"], errs["G"]+errs["H"]+errs["N"]+errs["G2"])
	fmt.Printf("  in flight when the server went: SIGKILL %d of 6 ok, graceful roll %d of 6 ok\n", ok["A"], ok["G"])
	fmt.Printf("  `rejected by transport` reported: %d (SIGKILL A %d, graceful G %d); `session not found` reported to the model: %d\n",
		transport, countIn(full["A"], "rejected by transport"), countIn(full["G"], "rejected by transport"), sessionNotFound)
	fmt.Printf("  Runs not ended %d, threads that end on a user message %d, tool errors with no text %d\n", stuck, unanswered, emptyErr)
	if data, err := os.ReadFile(logPath); err == nil { //nolint:gosec // our own temp directory
		for l := range strings.SplitSeq(string(data), "\n") {
			if strings.Contains(l, "msg=draining ") || strings.Contains(l, "msg=drained ") {
				fmt.Printf("  old fakemcp pod %s: %s\n", oldPod, trunc(l, 220))
			}
		}
	}
	fmt.Printf("  fakemcp now (new pod): %s\n", e.mcpStats(ctx))

	// KNOWN-MCP-GAP: a stateful server (the SDK's default) keeps its sessions in the pod. While the old and the new
	// pod both answer, a session can be sent to the pod that does not know it. Not the bot's and not a fault
	// of the roll itself; a stateless server (MCP_STATELESS=true) has none. Once the old pod is gone it must not happen.
	snf := func(phases ...string) int {
		n := 0
		for _, ph := range phases {
			n += countIn(full[ph], "session not found")
		}
		return n
	}
	during, after := snf("G", "H"), snf("N", "G2")
	if during > 0 {
		fmt.Printf("  KNOWN-MCP-GAP x%d: `session not found` reached the model while the old and the new pod both answered (G %d, H %d); after the roll (N, G2) %d\n", during, snf("G"), snf("H"), after)
	}
	// What fails the scenario: a Run that does not end, a thread left on a user message, a tool error with no
	// text, `rejected by transport` or a call that did not finish ok in the graceful in-flight calls for any reason
	// other than the session gap above, and any error in the calls sent after the roll.
	faults += stuck + emptyErr
	if unanswered > 0 {
		faults += unanswered
		fmt.Printf("  %s %d thread(s) end on a user message with no reply\n", fault, unanswered)
	}
	if n := countIn(full["G"], "rejected by transport"); n > 0 {
		faults += n
		fmt.Printf("  %s %d call(s) in flight at the graceful roll were cut (`rejected by transport`)\n", fault, n)
	}
	if n := errs["N"] + errs["G2"]; n > 0 {
		faults += n
		fmt.Printf("  %s %d call(s) after the roll did not succeed on the first try (`session not found` among them: %d)\n", fault, n, after)
	}
	return verdict("s10", faults == 0, fmt.Sprintf("%d GOTATO-FAULT", faults))
}

// errorsByPhase groups the model's tool-error reports by phase: counts by text cut to 110 characters (to
// print), and the full texts (to look inside).
func errorsByPhase(ts []*thread, rec func(*thread) record) (cut map[string]map[string]int, full map[string][]string) {
	cut, full = map[string]map[string]int{}, map[string][]string{}
	for _, t := range ts {
		for ph, texts := range errorTextsByPhase(rec(t)) {
			if cut[ph] == nil {
				cut[ph] = map[string]int{}
			}
			for _, text := range texts {
				cut[ph][trunc(text, 110)]++
				full[ph] = append(full[ph], text)
			}
		}
	}
	return cut, full
}

// countIn counts the texts that contain sub.
func countIn(texts []string, sub string) int {
	n := 0
	for _, text := range texts {
		if strings.Contains(text, sub) {
			n++
		}
	}
	return n
}

// killContainer stops the app's container with no grace period (SIGKILL); its pod starts it again.
func (e *env) killContainer(ctx context.Context, app string) (string, error) {
	pod, err := e.kubectl(ctx, "get", "pods", "-l", "app="+app, "-o", "jsonpath={.items[0].metadata.name}")
	if err != nil {
		return pod, err
	}
	const node = "gotato-control-plane"
	ids, err := exec.CommandContext(ctx, "docker", "exec", node, "crictl", "ps", "-q", "--name", app, "--label", "io.kubernetes.pod.name="+pod).Output() //nolint:gosec // pod names come from kubectl
	id := strings.TrimSpace(string(ids))
	if err != nil {
		return "crictl ps failed", fmt.Errorf("crictl ps: %w", err)
	}
	if id == "" {
		return "no container found", fmt.Errorf("crictl ps: no running %s container in pod %s", app, pod)
	}
	out, err := exec.CommandContext(ctx, "docker", "exec", node, "crictl", "stop", "-t", "0", id).CombinedOutput() //nolint:gosec // id comes from crictl
	return strings.TrimSpace(string(out)), err
}

// followFile writes a pod's log to path while the pod lives (and after SIGTERM, until it is gone).
func (e *env) followFile(ctx context.Context, pod, path string) func() {
	cctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		f, err := os.Create(path) //nolint:gosec // our own temp directory
		if err != nil {
			return
		}
		defer func() { _ = f.Close() }()
		cmd := exec.CommandContext(cctx, "kubectl", "--kubeconfig", e.kubeconfig, "--context", e.context, "-n", e.namespace, "logs", "-f", pod) //nolint:gosec // pod names come from kubectl
		cmd.Stdout = f
		_ = cmd.Run()
	}()
	return func() {
		// The log ends by itself when the container exits; give it a moment, then stop.
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
		cancel()
		<-done
	}
}

// waitReadyPods waits until every pod of the app is Ready and none is terminating.
func (e *env) waitReadyPods(ctx context.Context, app string, limit time.Duration) {
	for deadline := time.Now().Add(limit); time.Now().Before(deadline) && ctx.Err() == nil; sleep(ctx, time.Second) {
		out, err := e.kubectl(ctx, "get", "pods", "-l", "app="+app, "-o",
			`jsonpath={range .items[*]}{.metadata.deletionTimestamp}{"/"}{range .status.conditions[?(@.type=="Ready")]}{.status}{end}{"\n"}{end}`)
		lines := strings.Fields(out)
		if err == nil && len(lines) > 0 && !strings.Contains(out, "False") {
			ready := true
			for _, l := range lines {
				ready = ready && l == "/True"
			}
			if ready {
				return
			}
		}
	}
}
