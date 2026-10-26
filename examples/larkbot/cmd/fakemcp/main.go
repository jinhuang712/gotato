// Command fakemcp serves internal/fakemcp for the cluster: a tiny MCP server over streamable HTTP at /mcp.
// GET /stats counts the calls this process has seen. Not part of the bot.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jinhuang712/gotato/examples/larkbot/internal/fakemcp"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fakemcp", "err", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", "127.0.0.1:8090", "listen address")
	stateless := flag.Bool("stateless", false, "stateless streamable HTTP: no Mcp-Session-Id, so any replica can answer any call")
	drain := flag.Duration("drain", 0, "on SIGTERM, the most time to let calls in flight finish, as a server that rolls gracefully does (keep it under the pod's terminationGracePeriodSeconds); 0 stops at once")
	linger := flag.Duration("linger", 3*time.Second, "on SIGTERM, how long no call must be in flight before the server stops, for calls already on their way")
	trace := flag.Bool("trace", false, "log one line per /mcp request: method, session, status, time taken")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	mcp := fakemcp.New(log, *stateless)
	if *trace {
		mcp.Trace()
	}
	srv := &http.Server{Addr: *addr, Handler: mcp, ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		// Let the calls in flight finish before the listener closes: http.Server.Shutdown alone would wait for
		// every open connection, and an MCP client keeps a stream open for as long as it lives.
		if *drain <= 0 {
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdown)
			return
		}
		log.Info("draining", "in_flight", mcp.Stats().InFlight)
		drainCtx, stopDrain := context.WithTimeout(context.Background(), *drain)
		d := mcp.Drain(drainCtx, *linger)
		stopDrain()
		log.Info("drained", "in_flight_at_sigterm", d.InFlightAtStart, "started_during", d.StartedDuring, "finished_during", d.FinishedDuring, "left", d.Left, "took", d.Took.Round(time.Millisecond))
		shutdown, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if srv.Shutdown(shutdown) != nil {
			_ = srv.Close()
		}
	}()
	log.Info("fakemcp", "addr", *addr, "stateless", *stateless)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
