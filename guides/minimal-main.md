# A minimal main

By the end of this page you have a complete `main.go` that serves an agent over HTTP and stops without losing a Run when it gets SIGTERM, as a rolling deploy sends it. You need [HTTP in five minutes](http-in-five-minutes.md) first.

- [The program](#the-program)
- [The shutdown order](#the-shutdown-order)
- [Run it and send SIGTERM](#run-it-and-send-sigterm)
- [Your own Store](#your-own-store)

## The program

The program builds a Store, a Runner and a server, waits for a signal, and then stops in three steps. It was built and run as written.

```go
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/httpapi"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	// The Store is the truth: a directory here, your own session.Store later.
	store, err := session.NewFileStore(os.Getenv("SESSIONS_DIR"))
	if err != nil {
		return err
	}

	runner, err := service.New(service.Config{
		Store: store,
		Agents: []service.Agent{{
			Name:        "default",
			Model:       testkit.DemoModel{}, // swap for a gateway model
			Instruction: "You are a helpful assistant.",
			Tools:       []gotato.Tool{testkit.DemoEchoTool()},
		}},
		DrainTimeout: 20 * time.Second, // how long Runs in flight may finish
	})
	if err != nil {
		return err
	}

	server := &http.Server{
		Addr:              ":8080",
		Handler:           httpapi.New(runner),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// SIGTERM or Ctrl-C ends ctx.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe() }()
	log.Println("serving on", server.Addr)

	select {
	case err := <-serveErr: // the listener failed, for example the port is taken
		_ = runner.Close(context.Background())
		return err
	case <-ctx.Done():
	}
	log.Println("shutting down")

	// 1. Drain: /readyz answers 503, no new Run starts, every Run in flight
	//    finishes, is saved and releases its Session.
	if err := runner.Drain(context.Background()); err != nil {
		log.Println("drain:", err)
	}
	// 2. Stop accepting and close the connections that are left.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Println("shutdown:", err)
	}
	// 3. Close removes this Instance's Liveness row, so other Instances take its Sessions over at once.
	return runner.Close(context.Background())
}
```

- `SESSIONS_DIR` must name an existing directory.
- The shutdown contexts come from `context.Background()`, not from `ctx`: `ctx` has ended by then, and a shutdown that used it would give up at once.
- `DrainTimeout` is the time `Drain` lets the Runs finish. It is ten seconds when unset; set it above your longest usual Run, and below your orchestrator's kill delay (Kubernetes: `terminationGracePeriodSeconds`).

## The shutdown order

Three steps, in this order. Each one makes the next safe.

| Step | Call | What it does |
|---|---|---|
| 1. Stop taking work, and finish what is going | `runner.Drain(ctx)` | Marks the Instance draining in its Liveness row. `/readyz` answers 503, so the load balancer sends traffic elsewhere. The Runner refuses new Runs (`draining`, marked not run, so a client sends them to another Instance). Each Run in flight finishes, is saved, and releases its Session. When `DrainTimeout` ends first, the Runs left are stopped, saved and released, and the client gets a `reconnect` event. |
| 2. Stop the server | `server.Shutdown(ctx)` | Closes the listener and the idle connections, and waits for the rest. After step 1 nothing is left to wait for but streams. |
| 3. Remove the Instance | `runner.Close(ctx)` | Removes the Liveness row, so other Instances take its Sessions over at once instead of waiting for the row to expire. |

- Why `Drain` is first: it is the step that keeps a Run safe, and it turns new work away at the Runner, before the listener closes. The package comment of [`service/httpapi`](../service/httpapi/handler.go) gives the same order: `Drain`, then `Shutdown`, then `Close`.
- Why `Close` is last: a closed Runner writes nothing more. Closing before the Runs end would end them unsaved.
- After `Drain` returns an error, still go on to step 2 and 3. The error is `context.DeadlineExceeded` when the timeout ended the wait; the Runs left were stopped, saved and released, and a client that streamed one gets a `reconnect` event.

## Run it and send SIGTERM

To see it work, use a model that takes time. This is the same program with `testkit.NewFakeModel(testkit.Text("slow answer"))` whose `Delay` is set to 1.5 seconds. A Run is started, SIGTERM follows 0.5 seconds later, and the probes and the Run answer as they should:

```text
$ ./app &                      # serving on :8080
$ curl -XPOST localhost:8080/v1/sessions/$ID/runs -d '{"prompt":"hi"}' &
$ kill -TERM %1                # 0.5 s later
$ curl -s -w ' %{http_code}\n' localhost:8080/readyz
{"active_runs":1,"active_sessions":1,"draining":true,"ok":false} 503
$ curl -i -XPOST localhost:8080/v1/sessions/$ID/runs -d '{"prompt":"again"}'
HTTP/1.1 503 Service Unavailable
Gotato-Not-Run: true
Gotato-Retryable: true
{"code":"draining","message":"draining: service: draining","not_run":true,"retryable":true}
run: completed slow answer      # the Run in flight finished
exit=0                          # the process exited by itself
```

The log of the process reads `serving on :8080`, then `shutting down`. The Run that was going finished with `completed` and was saved, new work was refused as not run, and the process exited with status 0.

## Your own Store

If the Store is a database, close its connection pool last of all, after `runner.Close` has returned: `Close` and the last saves of the Runs write to it. With Go's `database/sql`, that is a `defer db.Close()` placed before the Runner is built, because deferred calls run after `run` returns.

Gotato's own stores need no closing. To write your own, see [Write a Store](write-a-store.md).

## Next

- Test this wiring without a provider: [Test with fakes](test-with-fakes.md).
- Run it on several Instances: [Embed Gotato in a service](embed-in-a-service.md).
