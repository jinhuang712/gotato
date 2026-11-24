// Command larkbot is an example of embedding Gotato in a Go service: a Lark
// (Feishu) chat bot with one Gotato Session per chat. It is not a Gotato
// product.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	httpapi "github.com/jinhuang712/gotato/adapter/http"
	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/host"
	"github.com/jinhuang712/gotato/profile"
	"github.com/jinhuang712/gotato/store"
	"github.com/jinhuang712/gotato/testkit"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "larkbot:", err)
		os.Exit(1)
	}
}

// config is what the flags and the environment say about one start.
type config struct {
	storeDir, model, once, addr, advertise, instance, label string
	delay                                                   time.Duration
	larkBase, larkEvents                                    string
	retryBudget, ackDelay                                   time.Duration
	toolList, mcpURL                                        string
	script                                                  bool
	appID, appSecret                                        string // from $LARK_APP_ID and $LARK_APP_SECRET
}

// parseFlags registers the flags, parses the command line and returns the result.
func parseFlags() config {
	var c config
	flag.StringVar(&c.storeDir, "store", "./data/sessions", "directory of the Gotato file store")
	flag.StringVar(&c.model, "model", "echo", "echo, demo, gateway (config: $GOTATO_GATEWAY_CONFIG or gateway.yaml)")
	flag.StringVar(&c.once, "once", "", "run this one prompt through the Runner, print the reply and exit (no Lark)")
	flag.StringVar(&c.addr, "addr", "127.0.0.1:8080", "internal HTTP address (/healthz, /readyz, /v1/...); empty turns it off")
	flag.StringVar(&c.advertise, "advertise", "", "address peers reach this instance at (default http://<addr>, with 127.0.0.1 for a bare :port)")
	flag.StringVar(&c.instance, "instance", "", "Gotato instance name in the store (default: generated at each start)")
	flag.StringVar(&c.label, "instance-label", "", "name shown in cards and logs (default $POD_NAME, else the host name)")
	flag.DurationVar(&c.delay, "delay", 0, "sleep this long in every model call (for tests)")
	flag.StringVar(&c.larkBase, "lark-base-url", "", "Lark open-platform base URL (default: real Lark); point it at cmd/fakelark")
	flag.StringVar(&c.larkEvents, "lark-events", "ws", "how events arrive: ws (long connection) or http (POST /lark/event on -addr, for cmd/fakelark)")
	flag.DurationVar(&c.retryBudget, "retry-budget", defaultRetryBudget, "how long a message that was not run (not_writer, busy, draining, not connected) is retried; 0 turns retrying off")
	flag.DurationVar(&c.ackDelay, "ack-delay", 0, "test only: answer each event this long late, so a sender with an ack timeout (Lark: 3 s) delivers it again")
	flag.StringVar(&c.toolList, "tools", "", "extra tools, comma-separated: export (test tool with a 1 MB result), mcp (the -mcp-url server, always visible), mcp-search (the same, in the tool-search catalog)")
	flag.StringVar(&c.mcpURL, "mcp-url", "", "streamable HTTP endpoint of the MCP server for -tools mcp / mcp-search")
	flag.BoolVar(&c.script, "script", false, "test only: play [script:NAME] directives in a message with a scripted model (script.go); other messages go to -model")
	flag.Parse()
	return c
}

// checkLark validates the Lark flags and reads the app credentials from the environment.
func (c *config) checkLark() error {
	if c.larkEvents != "ws" && c.larkEvents != "http" {
		return fmt.Errorf("-lark-events must be ws or http, got %q", c.larkEvents)
	}
	if c.larkEvents == "http" && c.addr == "" {
		return errors.New("-lark-events http needs -addr")
	}
	c.appID, c.appSecret = os.Getenv("LARK_APP_ID"), os.Getenv("LARK_APP_SECRET")
	if c.appID == "" || c.appSecret == "" {
		return errors.New("LARK_APP_ID and LARK_APP_SECRET must be set")
	}
	return nil
}

func run() error {
	cfg := parseFlags()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if cfg.once != "" {
		return runOnce(cfg.model, cfg.once)
	}
	if err := cfg.checkLark(); err != nil {
		return err
	}

	agent, tools, closeTools, err := cfg.prepareAgent(log)
	if err != nil {
		return err
	}
	if closeTools != nil {
		defer func() { _ = closeTools() }()
	}
	runner, records, err := cfg.newRunner(agent)
	if err != nil {
		return err
	}
	if c, ok := closerOf(agent.Model); ok {
		defer func() { _ = c.Close() }() // models that hold a connection
	}
	lbl := instanceLabel(cfg.label, runner.Instance())
	log.Info("runner ready", "instance", lbl, "runner_instance", runner.Instance(), "advertise", advertiseAddr(cfg.advertise, cfg.addr), "agent", agent.Name, "store", cfg.storeDir, "tools", tools.names(), "script", cfg.script)
	b := cfg.newBot(runner, records, agent, lbl, log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// The reply loop outlives the connection, so Runs finishing during the drain still get answered.
	replyCtx, stopReplies := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() { b.replyLoop(replyCtx) })

	srv := cfg.serveHTTP(runner, b, log)
	wsErr := cfg.receiveEvents(ctx, log, b)
	shutdown(log, runner, srv, stopReplies, &wg)
	if errors.Is(wsErr, context.Canceled) {
		return nil
	}
	return wsErr
}

// prepareAgent builds the Agent with the tools, the script and the delay the flags ask for.
// The returned close func ends the MCP connection (nil when there is none).
func (c config) prepareAgent(log *slog.Logger) (profile.AgentProfile, toolSpec, func() error, error) {
	agent, err := buildAgent(c.model)
	if err != nil {
		return agent, toolSpec{}, nil, err
	}
	tools, err := parseTools(c.toolList, c.mcpURL)
	if err != nil {
		return agent, tools, nil, err
	}
	closeTools, err := tools.apply(&agent, log)
	if err != nil {
		return agent, tools, nil, err
	}
	if c.script {
		agent.Model = scriptModel{agent.Model}
	}
	agent.Model = withDelay(agent.Model, c.delay)
	return agent, tools, closeTools, nil
}

// newRunner opens the file store and builds the Runner over it. It returns the Records of the store for the bot.
func (c config) newRunner(agent profile.AgentProfile) (*host.Runner, store.Records, error) {
	st, err := store.NewFileStore(c.storeDir)
	if err != nil {
		return nil, nil, err
	}
	runner, err := host.New(host.Config{Store: store.Store{Records: st, Liveness: st}, Profiles: registryOf(agent), DefaultAgent: agent.Name, Instance: c.instance, Address: advertiseAddr(c.advertise, c.addr)})
	if err != nil {
		return nil, nil, err
	}
	return runner, st, nil
}

// newBot builds the bot over a Lark client with the retry and ack settings of the flags.
func (c config) newBot(runner *host.Runner, records store.Records, agent profile.AgentProfile, lbl string, log *slog.Logger) *bot {
	opts := []lark.ClientOptionFunc{lark.WithLogger(larkLogger{log, []string{c.appSecret}}), lark.WithLogLevel(larkcore.LogLevelInfo)}
	if c.larkBase != "" {
		opts = append(opts, lark.WithOpenBaseUrl(c.larkBase))
	}
	api := lark.NewClient(c.appID, c.appSecret, opts...)
	b := newBot(runner, records, larkSender{api}, agent.Name, agent.ModelName, lbl, log)
	b.retry = defaultRetry(c.retryBudget)
	b.ackDelay = c.ackDelay
	return b
}

// serveHTTP starts the internal HTTP server on -addr and returns it (nil when -addr is empty).
func (c config) serveHTTP(runner *host.Runner, b *bot, log *slog.Logger) *http.Server {
	if c.addr == "" {
		return nil
	}
	// Gotato's handler, without forwarding: the bot forwards with its own client.
	mux := http.NewServeMux()
	mux.Handle("/v1/", httpapi.New(runner))
	if c.larkEvents == "http" {
		mux.HandleFunc("POST /lark/event", larkEventHandler(b))
	}
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprintln(w, "ok") })
	mux.HandleFunc("/readyz", readyHandler(runner.Draining))
	srv := &http.Server{Addr: c.addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http", "err", err)
		}
	}()
	return srv
}

// receiveEvents takes Lark events until ctx ends: over HTTP they arrive at the server, otherwise over the long connection.
func (c config) receiveEvents(ctx context.Context, log *slog.Logger, b *bot) error {
	if c.larkEvents == "http" {
		log.Info("lark events over http", "path", "/lark/event", "addr", c.addr)
		<-ctx.Done()
		return nil
	}
	return runLongConnection(ctx, log, c.appID, c.appSecret, b)
}

// shutdown drains the Runner, stops the reply loop and the HTTP server, then closes the Runner.
func shutdown(log *slog.Logger, runner *host.Runner, srv *http.Server, stopReplies context.CancelFunc, wg *sync.WaitGroup) {
	log.Info("shutting down")
	drainCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := runner.Drain(drainCtx); err != nil {
		log.Error("drain", "err", err)
	}
	time.Sleep(200 * time.Millisecond) // let the reply loop read the last run_ended
	stopReplies()
	wg.Wait()
	if srv != nil {
		_ = srv.Shutdown(drainCtx)
	}
	if err := runner.Close(drainCtx); err != nil {
		log.Error("close", "err", err)
	}
}

// readyHandler answers a readiness probe: 503 while the Runner drains, so a
// load balancer or Service stops sending events here, and 200 otherwise.
func readyHandler(draining func() bool) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if draining() {
			http.Error(w, "draining", http.StatusServiceUnavailable)
			return
		}
		_, _ = fmt.Fprintln(w, "ready")
	}
}

// botLimits raises the FollowUp buffer from Gotato's 4: a user often sends
// several lines while the bot is still answering, and a message past the
// buffer is refused (limit_exceeded) with nothing to retry against, as the Run
// may last minutes.
func botLimits() *gotato.CoreLimits {
	return &gotato.CoreLimits{MaxFollowUpMessages: 32}
}

// instruction is the persona of the bot. Replace it with your own.
const instruction = "You are a helpful assistant that answers in a Lark chat. Keep replies short and friendly, and say so when you do not know."

// buildAgent returns the one Agent of this instance.
func buildAgent(model string) (profile.AgentProfile, error) {
	switch model {
	case "echo":
		return profile.AgentProfile{Name: "echo", Model: testkit.EchoModel{}, ModelName: "echo", Instruction: instruction, Limits: botLimits()}, nil
	case "demo":
		return profile.AgentProfile{Name: "demo", Model: testkit.DemoModel{}, ModelName: "demo", Instruction: instruction, Limits: botLimits(),
			Tools: []gotato.Tool{testkit.DemoEchoTool()}}, nil
	case "gateway":
		path := os.Getenv("GOTATO_GATEWAY_CONFIG")
		if path == "" {
			path = "gateway.yaml"
		}
		client, cfg, err := loadModel(path)
		if err != nil {
			return profile.AgentProfile{}, fmt.Errorf("gateway config %s: %w", path, err)
		}
		return profile.AgentProfile{Name: "gateway", Model: client, ModelName: cfg.Name, Instruction: instruction, Limits: botLimits()}, nil
	}
	return profile.AgentProfile{}, fmt.Errorf("unknown -model %q (echo, demo or gateway)", model)
}

// registryOf registers the Agent as the only AgentProfile of a Runner. The caller names it in Config.DefaultAgent.
func registryOf(agent profile.AgentProfile) *profile.Registry {
	r := profile.NewRegistry(nil)
	if err := r.RegisterAgentProfileInline(agent); err != nil {
		panic(err)
	}
	return r
}
