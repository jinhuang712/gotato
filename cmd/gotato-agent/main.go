package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/gateway"
	"github.com/jinhuang712/gotato/host"
	"github.com/jinhuang712/gotato/internal/testmodel"
	"github.com/jinhuang712/gotato/orchestration"
)

type echoTool struct{}

func (echoTool) Spec() gotato.ToolSpec {
	return gotato.ToolSpec{ID: "demo.echo", Name: "demo_echo", Description: "Returns the value passed to it.", InputSchema: []byte(`{"type":"object","required":["value"],"properties":{"value":{"type":"string"}},"additionalProperties":false}`)}
}

func (echoTool) Execute(ctx context.Context, use gotato.ToolUse, progress gotato.ToolProgress) (gotato.ToolResult, error) {
	if progress != nil {
		progress("demo tool started")
	}
	var input struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(use.ArgumentsJSON, &input); err != nil {
		return gotato.ToolResult{}, err
	}
	return gotato.ToolResult{Status: gotato.ToolResultOK, Content: []gotato.ContentPart{{Kind: gotato.ContentText, Text: input.Value}}}, nil
}

func watchHeartbeat(agent gotato.Agent) {
	source, ok := agent.(gotato.EventSource)
	if !ok {
		return
	}
	stream, err := source.Subscribe(context.Background())
	if err != nil {
		log.Printf("heartbeat subscribe: %v", err)
		return
	}
	go func() {
		defer stream.Close()
		for {
			event, err := stream.Next(context.Background())
			if err != nil {
				return
			}
			switch event.Kind {
			case gotato.EventTurnStart:
				log.Printf("[heartbeat] run=%s turn=%d started", event.RunID, event.Turn)
			case gotato.EventTurnEnd:
				summary, _ := json.Marshal(event.Payload["summary"])
				log.Printf("[heartbeat] run=%s turn=%d completed summary=%s", event.RunID, event.Turn, summary)
			case gotato.EventAgentEnd:
				log.Printf("[heartbeat] run=%s finished status=%v", event.RunID, event.Payload["status"])
			}
		}
	}()
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8787", "listen address")
	modelName := flag.String("model", "echo", "model kind: echo, demo, or gateway")
	gatewayConfigPath := flag.String("gateway-config", "gateway.yaml", "YAML configuration for the OpenAI-compatible Gateway")
	runTimeout := flag.Duration("run-timeout", 10*time.Minute, "maximum duration of one Run; 0 disables the limit")
	modelTimeout := flag.Duration("model-timeout", 5*time.Minute, "maximum duration of one Model call; 0 disables the limit")
	toolTimeout := flag.Duration("tool-timeout", 5*time.Minute, "maximum duration of one Tool call; 0 disables the limit")
	heartbeat := flag.Bool("heartbeat", false, "log a bounded summary at the end of every loop turn")
	flag.Parse()

	var gatewayModel gotato.Model
	if *modelName == "gateway" {
		config, err := gateway.LoadYAML(*gatewayConfigPath)
		if err != nil {
			log.Fatal(err)
		}
		client, err := gateway.New(config)
		if err != nil {
			log.Fatal(err)
		}
		gatewayModel = client
	}
	newModel := func() gotato.Model {
		switch *modelName {
		case "demo":
			return testmodel.DemoModel{}
		case "gateway":
			return gatewayModel
		default:
			return testmodel.EchoModel{}
		}
	}
	factory := func(ctx context.Context, request orchestration.Request, snapshot *gotato.CoreSnapshot) (gotato.Agent, error) {
		options := []gotato.Option{
			gotato.WithModel(newModel()),
			gotato.WithInstruction("You are the local Gotato reference agent."),
			gotato.WithDeadlines(*runTimeout, *modelTimeout, *toolTimeout),
		}
		if *modelName == "demo" {
			options = append(options, gotato.WithTool(echoTool{}))
		}
		if snapshot != nil {
			options = append(options, gotato.WithInitialSnapshot(*snapshot))
		}
		agent, err := gotato.NewAgent(options...)
		if err != nil {
			return nil, err
		}
		if *heartbeat {
			watchHeartbeat(agent)
		}
		return agent, nil
	}

	orchestrator := orchestration.New()
	if err := orchestrator.Register(orchestration.Definition{Name: "default", New: factory}); err != nil {
		log.Fatal(err)
	}
	server := host.NewServer(orchestrator)
	httpServer := &http.Server{Addr: *addr, Handler: server.Handler(), ReadHeaderTimeout: 5 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		drainCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Drain(drainCtx); err != nil {
			log.Printf("drain: %v", err)
		}
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelShutdown()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}()

	log.Printf("gotato-agent listening on http://%s (model=%s)", *addr, *modelName)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
