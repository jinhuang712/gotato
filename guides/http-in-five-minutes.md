# HTTP in five minutes

By the end of this page you have a Gotato agent behind an HTTP endpoint, and you have started a Run with `curl`, watched its Events stream, and seen what a second Instance answers when a Session is busy elsewhere. You need the [first page](getting-started.md) and Go 1.24. No provider key is needed.

- [1. Serve a Runner](#1-serve-a-runner)
- [2. Create a Session and run a prompt](#2-create-a-session-and-run-a-prompt)
- [3. Stream a Run](#3-stream-a-run)
- [4. Watch a Session](#4-watch-a-session)
- [5. Message a Run in flight](#5-message-a-run-in-flight)
- [6. Not writer and forwarding](#6-not-writer-and-forwarding)
- [7. Health and readiness](#7-health-and-readiness)
- [8. Telling an error apart](#8-telling-an-error-apart)

## 1. Serve a Runner

`httpapi.New` takes a `service.Runner` and returns an `http.Handler`. The whole program is a Store, a Runner and one line of `net/http`.

```go
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/httpapi"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

func main() {
	store, err := session.NewFileStore(os.TempDir() + "/gotato-sessions")
	if err != nil {
		log.Fatal(err)
	}
	runner, err := service.New(service.Config{
		Store: store,
		Agents: []service.Agent{{
			Name:  "default",
			Model: testkit.DemoModel{}, // answers "demo response: <text>"; "use-tool" calls demo.echo
			Tools: []gotato.Tool{testkit.DemoEchoTool()},
		}},
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Fatal(http.ListenAndServe(":8080", httpapi.New(runner)))
}
```

- The Agent is a definition; every Session you create runs one. Swap `testkit.DemoModel` for a model from the `gateway` package when you have a provider.
- This program has no graceful shutdown. [A minimal main](minimal-main.md) adds it.
- Authentication, rate limiting and logging are yours: wrap the handler in your own middleware. The handler has none.

## 2. Create a Session and run a prompt

A Session is created first, and a Run is a `POST` to it. Both answer JSON.

```bash
curl -s -XPOST localhost:8080/v1/sessions -d '{"id":"s1"}'
# {"id":"s1","record_epoch":"226076f33b4f00ac",...,"messages":0,"runs":0,...} (201)

curl -s -XPOST localhost:8080/v1/sessions/s1/runs -d '{"prompt":"hello"}'
# {"session_id":"s1","agent":"default","result":{"run_id":"run-...","status":"completed",...},"final_text":"demo response: hello",...}
```

- `id` is optional: leave the body as `{}` and Gotato picks one, returned in the body and in the `Session-Id` header.
- Creating an `id` that is taken answers 409 with the code `already_exists`.
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

- To resume, send `Last-Event-ID: 14`, or `?after=14&record_epoch=<epoch>` (the epoch is in the first `stream` event and in every Session summary). A stream asked to resume with the wrong epoch is refused, because the Session was deleted and created again.
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
- `/notices` queues a notice (a runtime fact for the model, not a user message). It starts no Run. [FEATURES.md → Steering a Run](../FEATURES.md#g-f26--steering-a-run-) has the rules.

## 6. Not writer and forwarding

A Session has one Writer at a time. With several Instances behind a load balancer, a message can reach an Instance that is not the one running the Session. That Instance answers 409 `not_writer` and names the Writer, and nothing ran.

For the Instance to name an address, give each one `service.Config.Address`: where a caller can reach it.

```go
runner, err := service.New(service.Config{
	Store:    store,
	Instance: "pod-a",                 // a label; each start adds a random suffix
	Address:  "http://10.0.0.7:8080",  // advertised in the Liveness row
	Agents:   agents,
})
```

Here is what a second Instance answers while the first runs the Session (a run of two Instances on one shared Store, verified):

```text
POST pod-b/v1/sessions/ID/messages  {"text":"also check X"}
409  Gotato-Not-Run: true   Gotato-Retryable: true   Gotato-Writer: http://127.0.0.1:58129
{"code":"not_writer","message":"not_writer: service: the session's run is going on another instance (http://127.0.0.1:58129)","not_run":true,"retryable":true,"writer":"http://127.0.0.1:58129"}
```

A client can resend to the `Gotato-Writer` address itself. Or the handler forwards the message once, with `WithForwarding`:

```go
allow := func(address string) bool { return strings.HasPrefix(address, "http://10.0.") }
handler := httpapi.New(runner, httpapi.WithForwarding(nil, allow))
```

- `WithForwarding(client, allow)` takes an HTTP client (`nil` uses one with a 30 second timeout) and an allow function.
- The address comes from the Store, and anyone who can write that row can name it. A forward goes only to an address for which `allow` returns true; for any other, the answer is `not_writer`, as without forwarding.
- A nil `allow` forwards nowhere. Pass the check you trust: a prefix of your cluster's network, or a lookup in your own list of Instances.
- A forwarded request carries the caller's headers, so your authentication runs at the Writer. It carries `Gotato-Forwarded: true`, and the Writer never forwards it again.
- Only a message, a notice and an abort are forwarded. A `/runs` request for a Session that another Instance is running does not answer `not_writer`: it waits for that Run to end, for at most `service.Liveness.Wait` (5 seconds unless set), then runs on this Instance, or is refused `busy` (409, not run, retryable). Send Runs through your load balancer, which hashes the `session-id` header so every request for one Session reaches one Instance.

With forwarding on, the same message to the second Instance answers the Writer's own `202 {"session_id":...,"queued":true}`.

## 7. Health and readiness

Two probes, for your orchestrator.

```bash
curl -s localhost:8080/healthz
# {"contract":"1","ok":true}
curl -s -w ' %{http_code}\n' localhost:8080/readyz
# {"active_runs":0,"active_sessions":0,"ok":true} 200
```

- `/healthz` is liveness. It stays 200 while the Instance drains, so the orchestrator does not restart an Instance that is finishing its Runs.
- `/readyz` is readiness. While the Runner drains it answers 503 with `"draining":true`, so the load balancer sends traffic elsewhere. It also answers 503 with `"lapsed":true` when the Instance lost its Liveness row (the Store did not answer in time), until it registers a new one.
- Point your probes at them, and send SIGTERM for a rolling deploy: [A minimal main](minimal-main.md) shows the shutdown order.

## 8. Telling an error apart

Every error body is `{"code", "message", "not_run"?, "retryable"?}`, and two headers repeat the runtime's answer.

| Header | Meaning |
|---|---|
| `Gotato-Not-Run: true` | Nothing happened. The request is safe to send again, here or elsewhere. It does not say that sending it again helps: a 404 is not run and fails the same everywhere. |
| `Gotato-Retryable: true` | Sending the same request again later may succeed. |

A client or load balancer sends a request to another Instance when both headers are set. For `not_writer` it goes to the `Gotato-Writer` address. Here is a refusal while the Instance drains:

```text
POST /v1/sessions/ID/runs  {"prompt":"again"}
503  Gotato-Not-Run: true   Gotato-Retryable: true
{"code":"draining","message":"draining: service: draining","not_run":true,"retryable":true}
```

[RUNBOOK.md](../RUNBOOK.md) has the table of every code with its status.

## Next

- Shut down in order, and keep a Run safe through a deploy: [A minimal main](minimal-main.md).
- Test a handler and a Runner without a provider: [Test with fakes](test-with-fakes.md).
- The route table and the rules of a stream: the package comment of [`service/httpapi`](../service/httpapi/handler.go).
