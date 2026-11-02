// Command refservice is the reference service that GOALS.md G-B10 (startup
// time) and G-B12 (footprint) measure: the smallest program a team would write
// to embed Gotato in a Go service. It imports the core, the Runner, a file
// store, the HTTP handler and a gateway model, and nothing else.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	httpapi "github.com/jinhuang712/gotato/adapter/http"
	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/host"
	"github.com/jinhuang712/gotato/model"
	"github.com/jinhuang712/gotato/profile"
	"github.com/jinhuang712/gotato/store"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "listen address")
	storeDir := flag.String("store", "", "session store directory (default: a temp dir)")
	endpoint := flag.String("endpoint", "http://127.0.0.1:9/v1/chat/completions", "model endpoint (never called by the benchmark)")
	modelName := flag.String("model", "reference", "model name")
	flag.Parse()

	if err := run(*addr, *storeDir, *endpoint, *modelName); err != nil {
		log.Fatal(err)
	}
}

func run(addr, storeDir, endpoint, modelName string) error {
	if storeDir == "" {
		dir, err := os.MkdirTemp("", "refservice-")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(dir) }()
		storeDir = dir
	}
	st, err := store.NewFileStore(storeDir)
	if err != nil {
		return err
	}
	m, err := model.New(model.ChatCompletions{}, model.Connection{Endpoint: endpoint},
		gotato.ModelConfig{Name: modelName, Facts: gotato.ModelFacts{Window: gotato.WindowUnknown()}})
	if err != nil {
		return err
	}
	profiles := profile.NewRegistry(nil)
	if err := profiles.RegisterAgentProfileInline(profile.AgentProfile{Name: "assistant", Model: m, ModelName: modelName}); err != nil {
		return err
	}
	runner, err := host.New(host.Config{
		Store:    store.Store{Records: st, Liveness: st},
		Profiles: profiles, DefaultAgent: "assistant",
	})
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	fmt.Printf("listening %s\n", ln.Addr())

	srv := &http.Server{Handler: httpapi.New(runner), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
