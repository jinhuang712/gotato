// Command gotato-grpc serves the Gotato session service over gRPC. It is the
// gRPC twin of `gotato serve`: the same service.Runner behind a different
// protocol adapter.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	gotato "github.com/jinhuang712/gotato"
	grpcadapter "github.com/jinhuang712/gotato/adapter/grpc"
	"github.com/jinhuang712/gotato/gateway"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
	"google.golang.org/grpc"
)

// shutdownTimeout bounds both the Runner drain and the gRPC graceful stop so
// a client that stopped reading cannot hold the process open.
const shutdownTimeout = 10 * time.Second

// defaultInstruction is the system instruction every non-gateway agent
// carries, matching the CLI and `gotato serve`.
const defaultInstruction = "You are a helpful assistant."

// builtinTools is the Tool surface every agent offers, matching the CLI and
// `gotato serve`: DemoEchoTool plus time.now. Tools are optional capabilities;
// a Session may deactivate any of them.
func builtinTools() []gotato.Tool {
	now, err := gotato.NewFuncTool("time.now", "Returns the current time in RFC 3339 format.", func(context.Context, struct{}) (string, error) {
		return time.Now().UTC().Format(time.RFC3339), nil
	})
	if err != nil {
		panic(err)
	}
	return []gotato.Tool{testkit.DemoEchoTool(), now}
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8788", "listen address")
	storeDir := flag.String("store", "", "session store directory (default $GOTATO_HOME/sessions or ~/.gotato/sessions)")
	gatewayConfig := flag.String("gateway-config", "", "YAML config for the gateway agent (default $GOTATO_GATEWAY_CONFIG or gateway.yaml)")
	maxRuns := flag.Int("max-runs", 0, "maximum concurrent runs; 0 disables the bound")
	flag.Parse()

	if *maxRuns < 0 {
		fmt.Fprintln(os.Stderr, "gotato-grpc: --max-runs cannot be negative")
		flag.Usage()
		os.Exit(2)
	}

	dir := *storeDir
	if dir == "" {
		if home := os.Getenv("GOTATO_HOME"); home != "" {
			dir = filepath.Join(home, "sessions")
		} else {
			userHome, err := os.UserHomeDir()
			if err != nil {
				log.Fatal(err)
			}
			dir = filepath.Join(userHome, ".gotato", "sessions")
		}
	}
	store, err := session.NewFileStore(dir)
	if err != nil {
		log.Fatal(err)
	}
	// The CLI and `gotato serve` register echo and demo with
	// defaultInstruction and builtinTools; this twin must expose the same
	// agents, so the instruction and Tool surface are mirrored here.
	tools := builtinTools()
	specs := []service.AgentSpec{
		{Name: "echo", Model: testkit.EchoModel{}, ModelName: "echo", Instruction: defaultInstruction, Tools: tools},
		{Name: "demo", Model: testkit.DemoModel{}, ModelName: "demo", Instruction: defaultInstruction, Tools: tools},
	}
	configPath := *gatewayConfig
	if configPath == "" {
		configPath = os.Getenv("GOTATO_GATEWAY_CONFIG")
	}
	explicit := configPath != ""
	if configPath == "" {
		configPath = "gateway.yaml"
	}
	// An explicitly requested gateway config must load; the default path is
	// best-effort, so a fresh checkout still serves echo and demo and a
	// malformed default file is only disabled, never fatal.
	var client *gateway.Client
	switch config, loadErr := gateway.LoadYAML(configPath); {
	case loadErr != nil && explicit:
		log.Fatalf("gateway config %s: %v", configPath, loadErr)
	case loadErr != nil:
		log.Printf("gateway agent disabled: gateway config %s: %v", configPath, loadErr)
	default:
		created, newErr := gateway.New(config)
		if newErr != nil && explicit {
			log.Fatalf("gateway config %s: %v", configPath, newErr)
		}
		if newErr != nil {
			log.Printf("gateway agent disabled: gateway config %s: %v", configPath, newErr)
			break
		}
		client = created
		specs = append(specs, service.AgentSpec{Name: "gateway", Model: client, ModelName: config.Model, Tools: tools})
	}
	runner, err := service.New(service.Config{Store: store, Specs: specs, Admission: service.Admission{MaxActiveRuns: *maxRuns}})
	if err != nil {
		log.Fatal(err)
	}

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	server := grpc.NewServer()
	grpcadapter.New(runner).Register(server)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		drainCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := runner.Drain(drainCtx); err != nil {
			log.Printf("drain: %v", err)
		}
		// Bound the graceful stop: a StreamRun blocked in Send against a
		// client that stopped reading would otherwise block it forever.
		stopped := make(chan struct{})
		go func() {
			server.GracefulStop()
			close(stopped)
		}()
		select {
		case <-stopped:
		case <-drainCtx.Done():
			log.Printf("graceful stop timed out after %s; forcing stop", shutdownTimeout)
			server.Stop()
			<-stopped
		}
	}()
	log.Printf("gotato-grpc listening on %s (store=%s agents=%v)", *addr, dir, runner.Agents())
	if err := server.Serve(listener); err != nil {
		log.Fatal(err)
	}
}
