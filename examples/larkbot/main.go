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

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/cmd/gatewayconf"
	"github.com/jinhuang712/gotato/profile"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/httpapi"
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

func run() error {
	storeDir := flag.String("store", "./data/sessions", "directory of the Gotato file store")
	model := flag.String("model", "echo", "echo, demo, gateway (config: $GOTATO_GATEWAY_CONFIG or gateway.yaml)")
	once := flag.String("once", "", "run this one prompt through the Runner, print the reply and exit (no Lark)")
	addr := flag.String("addr", "127.0.0.1:8080", "internal HTTP address (/healthz, /readyz, /v1/...); empty turns it off")
	advertise := flag.String("advertise", "", "address peers reach this instance at (default http://<addr>, with 127.0.0.1 for a bare :port)")
	instance := flag.String("instance", "", "Gotato instance name in the store (default: generated at each start)")
	label := flag.String("instance-label", "", "name shown in cards and logs (default $POD_NAME, else the host name)")
	delay := flag.Duration("delay", 0, "sleep this long in every model call (for tests)")
	larkBase := flag.String("lark-base-url", "", "Lark open-platform base URL (default: real Lark); point it at cmd/fakelark")
	larkEvents := flag.String("lark-events", "ws", "how events arrive: ws (long connection) or http (POST /lark/event on -addr, for cmd/fakelark)")
	retryBudget := flag.Duration("retry-budget", defaultRetryBudget, "how long a message that was not run (not_writer, busy, draining, not connected) is retried; 0 turns retrying off")
	ackDelay := flag.Duration("ack-delay", 0, "test only: answer each event this long late, so a sender with an ack timeout (Lark: 3 s) delivers it again")
	toolList := flag.String("tools", "", "extra tools, comma-separated: export (test tool with a 1 MB result), mcp (the -mcp-url server, always visible), mcp-search (the same, in the tool-search catalog)")
	mcpURL := flag.String("mcp-url", "", "streamable HTTP endpoint of the MCP server for -tools mcp / mcp-search")
	script := flag.Bool("script", false, "test only: play [script:NAME] directives in a message with a scripted model (script.go); other messages go to -model")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if *once != "" {
		return runOnce(*model, *once)
	}
	if *larkEvents != "ws" && *larkEvents != "http" {
		return fmt.Errorf("-lark-events must be ws or http, got %q", *larkEvents)
	}
	if *larkEvents == "http" && *addr == "" {
		return errors.New("-lark-events http needs -addr")
	}
	appID, appSecret := os.Getenv("LARK_APP_ID"), os.Getenv("LARK_APP_SECRET")
	if appID == "" || appSecret == "" {
		return errors.New("LARK_APP_ID and LARK_APP_SECRET must be set")
	}

	agent, err := buildAgent(*model)
	if err != nil {
		return err
	}
	tools, err := parseTools(*toolList, *mcpURL)
	if err != nil {
		return err
	}
	closeTools, err := tools.apply(&agent, log)
	if err != nil {
		return err
	}
	if closeTools != nil {
		defer func() { _ = closeTools() }()
	}
	if *script {
		agent.Model = scriptModel{agent.Model}
	}
	agent.Model = withDelay(agent.Model, *delay)
	st, err := store.NewFileStore(*storeDir)
	if err != nil {
		return err
	}
	runner, err := service.New(service.Config{Store: store.Store{Records: st, Liveness: st}, Profiles: registryOf(agent), DefaultAgent: agent.Name, Instance: *instance, Address: advertiseAddr(*advertise, *addr)})
	if err != nil {
		return err
	}
	if c, ok := closerOf(agent.Model); ok {
		defer func() { _ = c.Close() }() // models that hold a connection
	}
	lbl := instanceLabel(*label, runner.Instance())
	log.Info("runner ready", "instance", lbl, "runner_instance", runner.Instance(), "advertise", advertiseAddr(*advertise, *addr), "agent", agent.Name, "store", *storeDir, "tools", tools.names(), "script", *script)

	opts := []lark.ClientOptionFunc{lark.WithLogger(larkLogger{log, []string{appSecret}}), lark.WithLogLevel(larkcore.LogLevelInfo)}
	if *larkBase != "" {
		opts = append(opts, lark.WithOpenBaseUrl(*larkBase))
	}
	api := lark.NewClient(appID, appSecret, opts...)
	b := newBot(runner, st, larkSender{api}, agent.Name, agent.ModelName, lbl, log)
	b.retry = defaultRetry(*retryBudget)
	b.ackDelay = *ackDelay

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// The reply loop outlives the connection, so Runs finishing during the drain still get answered.
	replyCtx, stopReplies := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() { ; b.replyLoop(replyCtx) })

	var srv *http.Server
	if *addr != "" {
		// Gotato's handler, without forwarding: the bot forwards with its own client.
		mux := http.NewServeMux()
		mux.Handle("/v1/", httpapi.New(runner))
		if *larkEvents == "http" {
			mux.HandleFunc("POST /lark/event", larkEventHandler(b))
		}
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprintln(w, "ok") })
		mux.HandleFunc("/readyz", readyHandler(runner.Draining))
		srv = &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		go func() {
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("http", "err", err)
			}
		}()
	}

	var wsErr error
	if *larkEvents == "http" {
		log.Info("lark events over http", "path", "/lark/event", "addr", *addr)
		<-ctx.Done()
	} else {
		wsErr = runLongConnection(ctx, log, appID, appSecret, b)
	}

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
	if errors.Is(wsErr, context.Canceled) {
		return nil
	}
	return wsErr
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
		cfg, err := gatewayconf.Load(path)
		if err != nil {
			return profile.AgentProfile{}, fmt.Errorf("gateway config %s: %w", path, err)
		}
		client, err := cfg.New()
		if err != nil {
			return profile.AgentProfile{}, err
		}
		return profile.AgentProfile{Name: "gateway", Model: client, ModelName: cfg.Model.Name, Instruction: instruction, Limits: botLimits()}, nil
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
