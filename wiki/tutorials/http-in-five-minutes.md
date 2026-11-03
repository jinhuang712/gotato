# HTTP in five minutes

> [Wiki](../README.md) › [Tutorials](../README.md#tutorials) › HTTP in five minutes

By the end of this page you have a Gotato agent behind an HTTP endpoint, and you have started a Run with `curl`, watched its Events stream, and seen what a second Instance answers when a Session is busy elsewhere. You need the [first page](getting-started.md) and the Go version of [Install](../how-to/install.md). No provider key is needed.

- [1. Serve a Runner](#1-serve-a-runner)
- [2. Create a Session and run a prompt](#2-create-a-session-and-run-a-prompt)
- [3. Stream a Run](#3-stream-a-run)
- [4. Watch a Session](#4-watch-a-session)
- [5. Message a Run in flight](#5-message-a-run-in-flight)
- [6. Health and readiness](#6-health-and-readiness)

## 1. Serve a Runner

`httpapi.New` takes a `host.Runner` and returns an `http.Handler`. The whole program is a Store, a Runner and one line of `net/http`.

The HTTP API is a module of its own, so add it to your module first (see [Install](../how-to/install.md)): `go get github.com/jinhuang712/gotato/adapter/http`. Its package is named `http`, like `net/http`, so the program imports it as `httpapi`.

```go
package main

import (
	"log"
	"net/http"
	"os"

	httpapi "github.com/jinhuang712/gotato/adapter/http"
	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/host"
	"github.com/jinhuang712/gotato/profile"
	"github.com/jinhuang712/gotato/store"
	"github.com/jinhuang712/gotato/testkit"
)

func main() {
	st, err := store.NewFileStore(os.TempDir() + "/gotato-sessions")
	if err != nil {
		log.Fatal(err)
	}
	profiles := profile.NewRegistry(nil)
	if err := profiles.RegisterAgentProfileInline(profile.AgentProfile{
		Name:  "default",
		Model: testkit.DemoModel{}, // answers "demo response: <text>"; "use-tool" calls demo.echo
		Tools: []gotato.Tool{testkit.DemoEchoTool()},
	}); err != nil {
		log.Fatal(err)
	}
	runner, err := host.New(host.Config{
		Store:        store.Store{Records: st, Liveness: st},
		Profiles:     profiles,
		DefaultAgent: "default",
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Fatal(http.ListenAndServe(":8080", httpapi.New(runner)))
}
```

- The Agent is a definition (a `profile.AgentProfile`, registered by name); every Session you create runs one, the `DefaultAgent` when a request names none. Swap `testkit.DemoModel` for a model from the `model` package when you have a provider.
- This program has no graceful shutdown. [A minimal main](a-minimal-main.md) adds it.
- Authentication, rate limiting and logging are yours: wrap the handler in your own middleware. The handler has none.

## 2. Create a Session and run a prompt

A Session is created first, and a Run is a `POST` to it. Both answer JSON. The URL names an existing Session: a Run on an ID that was never created answers 404 `not_found`. (In Go, `Runner.Run` with an empty `SessionID` creates one for you; over HTTP you create it yourself.)

```bash
curl -s -XPOST localhost:8080/v1/sessions -d '{"id":"s1"}'
# {"id":"s1","record_epoch":"226076f33b4f00ac",...,"messages":0,"runs":0,...} (201): the listing columns and the epoch

curl -s -XPOST localhost:8080/v1/sessions/s1/runs -d '{"prompt":"hello"}'
# {"session_id":"s1","agent":"default","result":{"run_id":"run-...","status":"completed",...},"final_text":"demo response: hello",...}
```

- `id` is optional: leave the body as `{}` and Gotato picks one, returned in the body and in the `Session-Id` header.
- Creating an `id` that is taken answers 409 with the code `already_exists`.
- `metadata` is yours, except for keys that start with `gotato.`: those belong to the Runner, and a create that sets one answers 400 with the code `invalid_argument` and `not_run`, naming the key, and creates nothing. A Session has no instruction of its own on the wire: it runs the Agent it names (`agent`), and a run body can carry `agent` to change it.
- `GET /v1/sessions` lists Sessions a page at a time, the most recently updated first: `{"sessions":[...],"next":"..."}`. Send `limit` and, for the next page, `cursor` set to the last `next`; `next` is left out after the last page. It needs a store whose `Records` can list (`store.Lister`; the memory and file stores can), and answers 400 `not_supported` otherwise.
- `POST /v1/sessions` with `{"task":"morning-report","id":"morning-2026-11-03"}` creates a Session for a registered TaskSpec (its profile, narrowed, and its Background), in place of `agent`; run it by its ID as above. The body names a TaskSpec and never carries one, and a second create of the same `id` is 409 `already_exists`, so a scheduler's retry opens no second Session. `GET /v1/tasks` lists the names: `{"tasks":[{"name":"morning-report","agent":"support"}]}`.
- The body of a Run is `{"prompt": "..."}` or `{"continue": true}`, with an optional `timeout_ms`. A Run that outlasts it settles as `deadline_exceeded`.
- The Run holds the request open until it settles. A client that drops the request ends the Run: it settles as `canceled`.
- Unknown fields in a body are refused with 400, so a typo does not pass quietly.

The same Session goes on with the next prompt, here or on another Instance that shares the Store.

## 3. Stream a Run

`/runs/stream` takes the same body and answers a Server-Sent Events stream: one SSE event for each Gotato Event, and a last `result` event.

```bash
curl -s -N -XPOST localhost:8080/v1/sessions/s1/runs/stream -d '{"prompt":"use-tool"}' | grep '^event'
# event: session_claimed
# event: run_started
# event: message_committed
# event: turn_started
# ...
# event: tool_started
# event: tool_ended
# event: tool_result_committed
# ...
# event: text_delta
# event: reply_ended
# event: run_ended
# event: result
```

- Each SSE event is named by the Event's kind, and its `data:` line is the Event as JSON.
- A stored Event carries an `id:` line, its number in the Session's Record. A delta such as `text_delta` is only sent, so it has none.
- A Run refused before it starts (busy, draining, unknown Session) answers an ordinary error status, not a stream. A failure after the stream began arrives as an `error` event.
- If the Instance drains mid-Run, a `reconnect` event follows the `result`, with a delay for the client to wait before it connects again.

## 4. Watch a Session

`GET /v1/sessions/{id}/stream` only watches. It is separate from the `POST` that holds a Run open, so a front end can hold one `POST` for the Run and watch through a `GET` that it can drop and resume.

```bash
curl -s -N localhost:8080/v1/sessions/s1/stream
# event: stream
# id: 1
# event: session_created
# ...
```

- To resume, send `Last-Event-ID: 14`, or `?after=14&record_epoch=<epoch>` (the epoch is in the first `stream` event and in the summary a create or fork answers). A stream asked to resume with the wrong epoch is refused, because the Session was deleted and created again.
- A delta is not replayed on resume. The stored `reply_ended` Event holds the whole text, and a client replaces its partial text with it.
- `GET /v1/sessions/{id}/events?kind=tool_ended` returns the stored Events as JSON Lines, for a tool that reads history rather than watches.

## 5. Message a Run in flight

A message reaches a Run that is going. It takes `kind` as `steer` (the default; it changes the Run's course), `follow_up` (the Run picks it up when it would otherwise settle) or `interrupt`.

```bash
curl -s -XPOST localhost:8080/v1/sessions/s1/messages -d '{"kind":"steer","text":"also check the invoices"}'
# {"session_id":"s1","message_id":"message-...","queued":true}   (202)

curl -s -XPOST localhost:8080/v1/sessions/s1/abort
# 202 {"session_id":"s1","status":"abort_requested"}; the Run settles as canceled
```

- 202 means the message is queued for the Run in flight.
- With no Run going, a message starts one and the answer waits for it, like `/runs` (200 with the Run).
- `/notices` queues a notice (a runtime fact for the model, not a user message). It starts no Run. [FEATURES.md → F4.4: Notices](../../FEATURES.md#f44-notices) has the rules.


## 6. Health and readiness

Two probes, for your orchestrator.

```bash
curl -s localhost:8080/healthz
# {"contract":"1","ok":true}
curl -s -w ' %{http_code}\n' localhost:8080/readyz
# {"active_runs":0,"active_sessions":0,"ok":true} 200
```

- `/healthz` is liveness. It stays 200 while the Instance drains, so the orchestrator does not restart an Instance that is finishing its Runs.
- `/readyz` is readiness. While the Runner drains it answers 503 with `"draining":true`, so the load balancer sends traffic elsewhere. It also answers 503 with `"lapsed":true` when the Instance lost its Liveness row (the Store did not answer in time), until it registers a new one.
- Point your probes at them, and send SIGTERM for a rolling deploy: [A minimal main](a-minimal-main.md) shows the shutdown order.


## Next

- Several Instances behind a load balancer: [Forward messages to the writer](../how-to/forward-messages-to-the-writer.md). Telling a refusal from a failure: [Tell an error apart](../how-to/tell-an-error-apart.md).
- Shut down in order, and keep a Run safe through a deploy: [A minimal main](a-minimal-main.md).
- Test a handler and a Runner without a provider: [Test with fakes](../how-to/test-with-fakes.md).
- The route table and the rules of a stream: the package comment of [`adapter/http`](../../adapter/http/handler.go).
