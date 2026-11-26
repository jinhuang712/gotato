// Command fakemodel serves internal/fakemodel on a port, for a test that runs
// outside the Go process, such as the local cluster. See the package
// documentation for the endpoints and the quirks.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/jinhuang712/gotato/internal/fakemodel"
)

const (
	// readHeaderTimeout bounds the time a client may take to send its headers.
	readHeaderTimeout = 10 * time.Second
	// shutdownTimeout bounds the wait for open requests when the server stops.
	shutdownTimeout = 5 * time.Second
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "address to listen on")
	flag.Parse()

	server := &http.Server{
		Addr:              *addr,
		Handler:           fakemodel.New(),
		ReadHeaderTimeout: readHeaderTimeout,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	fmt.Fprintln(os.Stderr, "fakemodel: listening on", *addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, "fakemodel:", err)
		os.Exit(1)
	}
}
