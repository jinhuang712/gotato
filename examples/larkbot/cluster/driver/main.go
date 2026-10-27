// Command driver plays the cluster scenarios against the local kind cluster that cluster/run.sh builds:
// smoke, s1 (rolling deploy with Runs in flight), s2 (kill the pod that writes a Run), s3 (scale the bot
// 3 -> 1 -> 5), s4 (pods contend for one Session), s5 (late acks, so events are redelivered),
// s7 (MCP calls while the server is killed, the bot is deployed and the server is rolled gracefully) and s8 (a
// tool loaded with search_tools, after a takeover).
//
// It talks to the fake Lark's driver API and to the bots' Gotato HTTP API through `kubectl proxy`
// (the API server's service proxy, so any ready bot pod answers a read), follows the bot pods' logs
// into files, and checks each Session's Record. It exits 1 only for a GOTATO-FAULT, 2 on a usage or
// setup error. Standard library only.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// busyNoticePrefix is how the bot's busy notice starts (busyNotice in the bot's bot.go); the driver finds it in a card.
const busyNoticePrefix = "I am busy right now"

type env struct {
	kubeconfig, context, namespace string
	proxy                          string // http://127.0.0.1:PORT of kubectl proxy
	logDir                         string
	http                           *http.Client
	victimPod, victimIP            string // the pod s2 killed
	killMode                       string // delete or crash
}

func main() {
	kubeconfig := flag.String("kubeconfig", "", "kubeconfig of the kind cluster")
	kctx := flag.String("context", "kind-gotato", "kubectl context")
	ns := flag.String("namespace", "gotato", "namespace")
	kill := flag.String("kill", "delete", "s2, s8: how to kill the writer: delete (kubectl delete pod --grace-period=0 --force) or crash (SIGKILL the container, no SIGTERM)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: driver -kubeconfig FILE [-context C] [-namespace N] smoke|s1|...|s8")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *kubeconfig == "" || flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	os.Exit(drive(*kubeconfig, *kctx, *ns, *kill, flag.Arg(0)))
}

// drive runs one scenario and returns the exit code: 0, 1 for a GOTATO-FAULT, 2 for a setup error.
func drive(kubeconfig, kctx, ns, kill, scenario string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	e := &env{kubeconfig: kubeconfig, context: kctx, namespace: ns, killMode: kill, http: &http.Client{Timeout: 60 * time.Second}}
	dir, err := os.MkdirTemp("", "larkbot-cluster-logs-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "driver:", err)
		return 2
	}
	e.logDir = dir
	stopProxy, err := e.startProxy(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "driver:", err)
		return 2
	}
	stopLogs := e.followLogs(ctx)

	if !e.warmUp(ctx) {
		fmt.Printf("\nbot pod logs: %s\n", e.logDir)
		stopLogs()
		stopProxy()
		return 2
	}
	ok := run(ctx, e, scenario)
	fmt.Printf("\nbot pod logs: %s\n", e.logDir)
	stopLogs()
	stopProxy()
	if !ok {
		return 1
	}
	return 0
}

func run(ctx context.Context, e *env, scenario string) bool {
	var ok bool
	switch scenario {
	case "smoke":
		ok = e.smoke(ctx)
	case "s1":
		ok = e.s1(ctx)
	case "s2":
		ok = e.s2(ctx)
	case "s3":
		ok = e.s3(ctx)
	case "s4":
		ok = e.s4(ctx)
	case "s5":
		ok = e.s5(ctx)
	case "s7":
		ok = e.s7(ctx)
	case "s8":
		ok = e.s8(ctx)
	default:
		flag.Usage()
		os.Exit(2)
	}
	return ok
}

// kubectl runs kubectl against the kind cluster only.
func (e *env) kubectl(ctx context.Context, args ...string) (string, error) {
	full := append([]string{"--kubeconfig", e.kubeconfig, "--context", e.context, "-n", e.namespace}, args...)
	out, err := exec.CommandContext(ctx, "kubectl", full...).CombinedOutput() //nolint:gosec // our own arguments
	return strings.TrimSpace(string(out)), err
}

var proxyRe = regexp.MustCompile(`127\.0\.0\.1:(\d+)`)

func (e *env) startProxy(ctx context.Context) (func(), error) {
	cmd := exec.CommandContext(ctx, "kubectl", "--kubeconfig", e.kubeconfig, "--context", e.context, "proxy", "--port=0") //nolint:gosec // our own arguments
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	m := proxyRe.FindStringSubmatch(line)
	if err != nil || m == nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("kubectl proxy did not start: %q: %w", line, err)
	}
	e.proxy = "http://127.0.0.1:" + m[1]
	go func() { _, _ = io.Copy(io.Discard, out) }()
	return func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }, nil
}

func (e *env) svc(name string, port int) string {
	return fmt.Sprintf("%s/api/v1/namespaces/%s/services/%s:%d/proxy", e.proxy, e.namespace, name, port)
}

// getJSON and postJSON call a service through the proxy. A non-2xx answer is an error carrying the body.
func (e *env) do(ctx context.Context, method, url string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: %d %s", method, url, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// --- bot pod logs, followed into files so a deleted pod's lines survive ---

func (e *env) followLogs(ctx context.Context) func() {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	started := map[string]bool{}
	wg.Go(func() {
		for ctx.Err() == nil {
			out, err := e.kubectl(ctx, "get", "pods", "-l", "app=larkbot", "-o", "jsonpath={range .items[*]}{.metadata.name}{\"\\n\"}{end}")
			if err == nil {
				for pod := range strings.FieldsSeq(out) {
					if !started[pod] {
						started[pod] = true
						wg.Go(func() { e.followPod(ctx, pod) })
					}
				}
			}
			select {
			case <-ctx.Done():
			case <-time.After(500 * time.Millisecond):
			}
		}
	})
	return func() { cancel(); wg.Wait() }
}

// followPod appends a pod's log to a file for as long as the pod exists. A container that restarts in the
// same pod (a crash) is followed again from the last line's time, so its lines are not lost.
func (e *env) followPod(ctx context.Context, pod string) {
	path := filepath.Join(e.logDir, pod+".log")
	since := ""
	for ctx.Err() == nil {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // our own temp directory
		if err != nil {
			return
		}
		args := []string{"--kubeconfig", e.kubeconfig, "--context", e.context, "-n", e.namespace, "logs", "-f", pod}
		if since != "" {
			args = append(args, "--since-time="+since)
		}
		cmd := exec.CommandContext(ctx, "kubectl", args...) //nolint:gosec // pod names come from kubectl
		cmd.Stdout = f
		_ = cmd.Run()
		_ = f.Close()
		if t := lastLogTime(path); t != "" {
			since = t
		}
		if out, err := e.kubectl(ctx, "get", "pod", pod, "-o", "jsonpath={.metadata.deletionTimestamp}"); err != nil || out != "" {
			return // the pod is gone or going: its lines are in the file
		}
		sleep(ctx, time.Second)
	}
}

var timeRe = regexp.MustCompile(`time=(\S+)`)

func lastLogTime(path string) string {
	data, _ := os.ReadFile(path) //nolint:gosec // our own temp directory
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for _, line := range slices.Backward(lines) {
		if m := timeRe.FindStringSubmatch(line); m != nil {
			return m[1]
		}
	}
	return ""
}

// logLine is one slog text line of a bot.
type logLine struct {
	Pod, Msg string
	KV       map[string]string
}

var kvRe = regexp.MustCompile(`(\w+)=("(?:[^"\\]|\\.)*"|\S*)`)

func (e *env) readLogs() []logLine {
	files, _ := filepath.Glob(filepath.Join(e.logDir, "*.log"))
	var out []logLine
	for _, f := range files {
		pod := strings.TrimSuffix(filepath.Base(f), ".log")
		data, _ := os.ReadFile(f) //nolint:gosec // our own temp directory
		for l := range strings.SplitSeq(string(data), "\n") {
			if !strings.Contains(l, "level=") {
				continue
			}
			kv := map[string]string{}
			for _, m := range kvRe.FindAllStringSubmatch(l, -1) {
				v := m[2]
				if strings.HasPrefix(v, `"`) {
					var s string
					if json.Unmarshal([]byte(v), &s) == nil {
						v = s
					}
				}
				kv[m[1]] = v
			}
			out = append(out, logLine{Pod: pod, Msg: kv["msg"], KV: kv})
		}
	}
	return out
}

// pods lists the bot pods that exist now (name -> ready).
func (e *env) pods(ctx context.Context) (map[string]bool, error) {
	out, err := e.kubectl(ctx, "get", "pods", "-l", "app=larkbot", "-o",
		`jsonpath={range .items[*]}{.metadata.name}{" "}{.metadata.deletionTimestamp}{" "}{range .status.conditions[?(@.type=="Ready")]}{.status}{end}{"\n"}{end}`)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, out)
	}
	m := map[string]bool{}
	for l := range strings.SplitSeq(out, "\n") {
		f := strings.Fields(l)
		if len(f) == 0 {
			continue
		}
		m[f[0]] = f[len(f)-1] == "True" && len(f) == 2
	}
	return m, nil
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
