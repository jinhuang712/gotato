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

// mcpRoll is what s7's graceful phase leaves for its report: the old fakemcp pod and its log.
type mcpRoll struct {
	oldPod, logPath string
}

// rollMCP is the last phase of s7: a graceful roll of the MCP server, the way a deploy does it (rollout restart:
// SIGTERM, fakemcp drains, the new pod takes over), next to the abrupt kill of phase A. G are calls of 8 s at the
// server in flight when the roll starts, H is one new thread a second while it goes on, N are new threads after
// it, and G2 is a second message in the G threads.
func (e *env) rollMCP(ctx context.Context, st stamper, mk func(string, int) []*thread,
	sendAll func(ts []*thread, phase, suffix, directive string, gap time.Duration)) (roll mcpRoll, out string, err error) {
	e.waitReadyPods(ctx, "fakemcp", 60*time.Second)
	roll.oldPod, _ = e.kubectl(ctx, "get", "pods", "-l", "app=fakemcp", "-o", "jsonpath={.items[0].metadata.name}")
	roll.logPath = filepath.Join(e.logDir, "fakemcp-"+roll.oldPod+".log")
	stopFollow := e.followFile(ctx, roll.oldPod, roll.logPath)
	g := mk("G", 6)
	sendAll(g, "G", "a", "[script:mcp=8s]", 200*time.Millisecond)
	sleep(ctx, 2*time.Second)
	fmt.Printf("%s graceful: kubectl rollout restart deployment/fakemcp (old pod %s)\n", st.at(), roll.oldPod)
	rollAt := time.Now()
	if out, err := e.kubectl(ctx, "rollout", "restart", "deployment/fakemcp"); err != nil {
		stopFollow()
		return roll, out, err
	}
	// The roll lasts until the old pod is gone: `rollout status` returns once the new pod is ready, while the
	// old one is still draining its calls.
	rolled := make(chan error, 1)
	go func() {
		out, err := e.kubectl(ctx, "rollout", "status", "deployment/fakemcp", "--timeout=120s")
		if err == nil {
			out, err = e.kubectl(ctx, "wait", "--for=delete", "pod/"+roll.oldPod, "--timeout=60s")
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
			m := e.say(ctx, t, fmt.Sprintf("s7-H-%02d-a", t.n), "[script:mcp=2s]")
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
	return roll, "", nil
}

// reportRoll prints what the graceful phase of s7 shows and returns its GOTATO-FAULT count. ok and errs are the
// tool calls the model reported, by phase; full holds the texts of the errors, by phase.
func reportRoll(roll mcpRoll, ok, errs map[string]int, full map[string][]string) (faults int) {
	transport := func(ph string) int { return countIn(full[ph], "rejected by transport") }
	fmt.Printf("  SIGKILL (A, B, R): %d ok / %d error; graceful roll (G, H, N, G2): %d ok / %d error\n",
		ok["A"]+ok["B"]+ok["R"], errs["A"]+errs["B"]+errs["R"], ok["G"]+ok["H"]+ok["N"]+ok["G2"], errs["G"]+errs["H"]+errs["N"]+errs["G2"])
	fmt.Printf("  in flight when the server went: SIGKILL %d of 6 ok, graceful roll %d of 6 ok\n", ok["A"], ok["G"])
	sessionNotFound := 0
	for _, texts := range full {
		sessionNotFound += countIn(texts, "session not found")
	}
	fmt.Printf("  `rejected by transport` reported: SIGKILL A %d, graceful G %d; `session not found` reported to the model: %d\n",
		transport("A"), transport("G"), sessionNotFound)
	if data, err := os.ReadFile(roll.logPath); err == nil { //nolint:gosec // our own temp directory
		for l := range strings.SplitSeq(string(data), "\n") {
			if strings.Contains(l, "msg=draining ") || strings.Contains(l, "msg=drained ") {
				fmt.Printf("  old fakemcp pod %s: %s\n", roll.oldPod, trunc(l, 220))
			}
		}
	}
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
	// What fails the scenario here: `rejected by transport` in the calls in flight at the graceful roll, and any
	// error in the calls sent after the roll.
	if n := transport("G"); n > 0 {
		faults += n
		fmt.Printf("  %s %d call(s) in flight at the graceful roll were cut (`rejected by transport`)\n", fault, n)
	}
	if n := errs["N"] + errs["G2"]; n > 0 {
		faults += n
		fmt.Printf("  %s %d call(s) after the roll did not succeed on the first try (`session not found` among them: %d)\n", fault, n, after)
	}
	return faults
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
