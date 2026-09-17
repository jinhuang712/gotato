// Command gotato-grpc serves the Gotato session service over gRPC. It is the
// gRPC twin of `gotato serve`: the same service.Runner behind a different
// protocol adapter.
package main

import (
	"context"
	"flag"
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

func main() {
	addr := flag.String("addr", "127.0.0.1:8788", "listen address")
	storeDir := flag.String("store", "", "session store directory (default $GOTATO_HOME/sessions or ~/.gotato/sessions)")
	gatewayConfig := flag.String("gateway-config", "", "YAML config for the gateway agent (default $GOTATO_GATEWAY_CONFIG or gateway.yaml)")
	maxRuns := flag.Int("max-runs", 0, "maximum concurrent runs; 0 disables the bound")
	flag.Parse()

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
	tools := []gotato.Tool{testkit.DemoEchoTool()}
	specs := []service.AgentSpec{
		{Name: "echo", Model: testkit.EchoModel{}, ModelName: "echo", Tools: tools},
		{Name: "demo", Model: testkit.DemoModel{}, ModelName: "demo", Tools: tools},
	}
	configPath := *gatewayConfig
	if configPath == "" {
		configPath = os.Getenv("GOTATO_GATEWAY_CONFIG")
	}
	if configPath == "" {
		configPath = "gateway.yaml"
	}
	if config, err := gateway.LoadYAML(configPath); err == nil {
		if client, err := gateway.New(config); err == nil {
			specs = append(specs, service.AgentSpec{Name: "gateway", Model: client, ModelName: config.Model, Tools: tools})
		}
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
		drainCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := runner.Drain(drainCtx); err != nil {
			log.Printf("drain: %v", err)
		}
		server.GracefulStop()
	}()
	log.Printf("gotato-grpc listening on %s (store=%s agents=%v)", *addr, dir, runner.Agents())
	if err := server.Serve(listener); err != nil {
		log.Fatal(err)
	}
}
