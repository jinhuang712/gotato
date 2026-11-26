package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	httpapi "github.com/jinhuang712/gotato/adapter/http"
	"github.com/jinhuang712/gotato/host"
)

// The time a gotato serve waits for a client to send its request headers, and
// for the server to shut down.
const (
	readHeaderTimeout = 5 * time.Second
	shutdownTimeout   = 5 * time.Second
)

// serveFlags are the flags of gotato serve beside the model flags.
type serveFlags struct {
	addr             string
	maxRuns          int
	maxSessions      int
	queue            string
	warmTTL          time.Duration
	noIdleCompaction bool
	drain            time.Duration
	advertise        string
	forward          bool
}

func bindServe(fs *flag.FlagSet, f *serveFlags) {
	fs.StringVar(&f.addr, "addr", "127.0.0.1:8787", "listen address")
	fs.IntVar(&f.maxRuns, "max-runs", 0, "maximum concurrent runs; 0 disables the bound")
	fs.IntVar(&f.maxSessions, "max-sessions", 0, "hard limit on active sessions; past it a new session is refused with a 503 marked not run; 0 disables the limit")
	fs.StringVar(&f.queue, "queue", "reject", "policy for a busy session: reject or wait")
	fs.DurationVar(&f.warmTTL, "warm-ttl", 0, "how long an idle session stays in memory after its run; 0 is 60m, a negative value keeps none")
	fs.BoolVar(&f.noIdleCompaction, "no-idle-compaction", false, "do not compact an idle session shortly before its prompt cache expires or it leaves memory")
	fs.DurationVar(&f.drain, "drain-timeout", 0, "time to wait for active runs on shutdown before canceling them; 0 is the service's default (10s), a negative value waits for ever")
	fs.StringVar(&f.advertise, "advertise", "", "URL other callers reach this instance at, such as http://10.0.0.7:8787; a message for a run this instance writes, sent to another instance, is answered not_writer with it")
	fs.BoolVar(&f.forward, "forward", false, "tests only: send a message or cancel for a run another instance writes to that instance's advertised address, instead of answering not_writer; it forwards to any address in the store, so do not use it in production")
}

// problem says what is wrong with the flags, or is empty when they are valid.
func (f serveFlags) problem() string {
	switch f.queue {
	case string(host.QueuePolicyReject), string(host.QueuePolicyWait):
	default:
		return "--queue accepts reject or wait"
	}
	if f.maxRuns < 0 {
		return "--max-runs cannot be negative"
	}
	if f.maxSessions < 0 {
		return "--max-sessions cannot be negative"
	}
	return ""
}

// hostConfig is base with the admission settings the flags ask for.
func (f serveFlags) hostConfig(base host.Config) host.Config {
	cfg := base
	cfg.Warm = host.WarmPolicy{TTL: f.warmTTL, NoIdleCompaction: f.noIdleCompaction}
	cfg.Address, cfg.DrainTimeout = f.advertise, f.drain
	cfg.Bounds = host.Bounds{MaxActiveSessions: f.maxSessions, Queue: host.QueuePolicy(f.queue)}
	cfg.Admitter = host.Limits{MaxActiveRuns: f.maxRuns}
	return cfg
}

// handlerOptions are the options of the HTTP handler the flags ask for.
func (f serveFlags) handlerOptions() []httpapi.Option {
	var options []httpapi.Option
	if f.forward {
		// A test tool on a trusted store: any writer address is allowed.
		options = append(options, httpapi.WithForwarding(nil, func(string) bool { return true }))
	}
	return options
}

// cmdServe runs the same Runner behind the HTTP API.
func (c *cli) cmdServe(args []string) int {
	fs := c.newFlagSet("serve")
	var flags modelFlags
	bindModel(fs, &flags)
	var serve serveFlags
	bindServe(fs, &serve)
	if _, err := parseInterspersed(fs, args); err != nil {
		return c.parseError(err)
	}
	if problem := serve.problem(); problem != "" {
		return c.usageError(problem)
	}
	rt, err := c.newRuntime(flags)
	if err != nil {
		return c.failErr(err)
	}
	// Rebuild with admission settings: the Runner is cheap to construct.
	runner, err := host.New(serve.hostConfig(rt.config))
	if err != nil {
		return c.failErr(err)
	}
	// closeRunners removes its Liveness row when serve returns, also after a
	// failed listen.
	c.runners = append(c.runners, runner)
	server := &http.Server{Addr: serve.addr, Handler: httpapi.New(runner, serve.handlerOptions()...), ReadHeaderTimeout: readHeaderTimeout}
	ctx, stop := c.serveContext()
	defer stop()
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()
	c.announceServing(serve.addr, rt.storeDir, runner)
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return c.fail(ExitError, err.Error())
		}
		return ExitOK
	case <-ctx.Done():
	}
	c.shutDown(server, runner)
	return ExitOK
}

// serveContext is canceled by an interrupt or SIGTERM and, when --timeout is
// set, when it elapses.
func (c *cli) serveContext() (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// --timeout bounds the process lifetime: when it elapses the server drains
	// and shuts down like a signal. A signal still works when --timeout is 0.
	if c.timeout > 0 {
		timeoutCtx, timeoutCancel := context.WithTimeout(ctx, c.timeout)
		return timeoutCtx, func() {
			timeoutCancel()
			stop()
		}
	}
	return ctx, stop
}

// announceServing tells that the server listens, on stderr and, in machine
// mode, as a JSON Line on stdout.
func (c *cli) announceServing(addr, storeDir string, runner *host.Runner) {
	c.info("gotato serving on http://%s (store=%s agents=%s contract=%s)", addr, storeDir, strings.Join(runner.AgentProfiles(), ","), httpapi.ContractVersion)
	if c.machine() {
		_ = c.writeJSONL(map[string]any{"kind": "serving", "addr": addr, "store": storeDir, "agents": runner.AgentProfiles(), "contract": httpapi.ContractVersion})
	}
}

// shutDown drains the Runner, stops the server and closes the Runner.
func (c *cli) shutDown(server *http.Server, runner *host.Runner) {
	// Drain in order: the Liveness row says draining, /readyz answers 503, no
	// new Run starts, and every Run finishes, saves and releases its Session.
	// Then the streams close, and Close removes the row.
	c.info("draining")
	if err := runner.Drain(context.Background()); err != nil {
		c.info("drain: %v", err)
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancelShutdown()
	_ = server.Shutdown(shutdownCtx)
	closeCtx, cancelClose := context.WithTimeout(context.Background(), closeTimeout)
	defer cancelClose()
	if err := runner.Close(closeCtx); err != nil {
		c.info("close: %v", err)
	}
	c.info("stopped")
}
