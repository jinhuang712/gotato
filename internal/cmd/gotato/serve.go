package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/httpapi"
)

// cmdServe runs the same Runner behind the HTTP API.
func (c *cli) cmdServe(args []string) int {
	fs := c.newFlagSet("serve")
	var flags modelFlags
	bindModel(fs, &flags)
	addr := fs.String("addr", "127.0.0.1:8787", "listen address")
	maxRuns := fs.Int("max-runs", 0, "maximum concurrent runs; 0 disables the bound")
	maxSessions := fs.Int("max-sessions", 0, "hard limit on active sessions; past it a new session is refused with a 503 marked not run; 0 disables the limit")
	queue := fs.String("queue", "reject", "policy for a busy session: reject or wait")
	warmTTL := fs.Duration("warm-ttl", 0, "how long an idle session stays in memory after its run; 0 is 60m, a negative value keeps none")
	noIdleCompaction := fs.Bool("no-idle-compaction", false, "do not compact an idle session shortly before its prompt cache expires or it leaves memory")
	drain := fs.Duration("drain-timeout", 0, "time to wait for active runs on shutdown before canceling them; 0 is the service's default (10s), a negative value waits for ever")
	advertise := fs.String("advertise", "", "URL other callers reach this instance at, such as http://10.0.0.7:8787; a message for a run this instance writes, sent to another instance, is answered not_writer with it")
	forward := fs.Bool("forward", false, "send a message or cancel for a run another instance writes to that instance's advertised address, instead of answering not_writer")
	if _, err := parseInterspersed(fs, args); err != nil {
		return c.parseError(err)
	}
	switch *queue {
	case string(service.QueuePolicyReject), string(service.QueuePolicyWait):
	default:
		return c.usageError("--queue accepts reject or wait")
	}
	if *maxRuns < 0 {
		return c.usageError("--max-runs cannot be negative")
	}
	if *maxSessions < 0 {
		return c.usageError("--max-sessions cannot be negative")
	}
	rt, err := c.newRuntime(flags)
	if err != nil {
		return c.failErr(err)
	}
	// Rebuild with admission settings: the Runner is cheap to construct.
	specs := make([]service.Agent, 0)
	for _, name := range rt.runner.Agents() {
		spec, _ := rt.runner.Agent(name)
		spec.Warm = service.WarmPolicy{TTL: *warmTTL, NoIdleCompaction: *noIdleCompaction}
		specs = append(specs, spec)
	}
	runner, err := service.New(service.Config{Store: rt.store, Agents: specs, Address: *advertise, DrainTimeout: *drain, Admission: service.Admission{MaxActiveRuns: *maxRuns, MaxActiveSessions: *maxSessions, Queue: service.QueuePolicy(*queue)}})
	if err != nil {
		return c.failErr(err)
	}
	// closeRunners removes its Liveness row when serve returns, also after a
	// failed listen.
	c.runners = append(c.runners, runner)
	var handlerOptions []httpapi.Option
	if *forward {
		// A test tool on a trusted store: any writer address is allowed.
		handlerOptions = append(handlerOptions, httpapi.WithForwarding(nil, func(string) bool { return true }))
	}
	server := &http.Server{Addr: *addr, Handler: httpapi.New(runner, handlerOptions...), ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// --timeout bounds the process lifetime: when it elapses the server drains
	// and shuts down like a signal. A signal still works when --timeout is 0.
	if c.timeout > 0 {
		var timeoutCancel context.CancelFunc
		ctx, timeoutCancel = context.WithTimeout(ctx, c.timeout)
		defer timeoutCancel()
	}
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()
	c.info("gotato serving on http://%s (store=%s agents=%s contract=%s)", *addr, rt.storeDir, strings.Join(runner.Agents(), ","), httpapi.ContractVersion)
	if c.machine() {
		_ = c.writeJSONL(map[string]any{"kind": "serving", "addr": *addr, "store": rt.storeDir, "agents": runner.Agents(), "contract": httpapi.ContractVersion})
	}
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return c.fail(ExitError, err.Error())
		}
		return ExitOK
	case <-ctx.Done():
	}
	// Drain in order: the Liveness row says draining, /readyz answers 503, no
	// new Run starts, and every Run finishes, saves and releases its Session.
	// Then the streams close, and Close removes the row.
	c.info("draining")
	if err := runner.Drain(context.Background()); err != nil {
		c.info("drain: %v", err)
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	_ = server.Shutdown(shutdownCtx)
	closeCtx, cancelClose := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelClose()
	if err := runner.Close(closeCtx); err != nil {
		c.info("close: %v", err)
	}
	c.info("stopped")
	return ExitOK
}
